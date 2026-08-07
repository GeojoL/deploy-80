//go:build integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mergeDBFixture struct {
	runID       string
	source      string
	destination string
	sourceDB    string
	targetDB    string
	previewRoot string
}

func TestMigrateSongsIntegration(t *testing.T) {
	if os.Getenv("DP80_INTEGRATION") != "1" {
		t.Skip("set DP80_INTEGRATION=1 to run isolated PostgreSQL integration tests")
	}
	image := os.Getenv("DP80_TEST_PG_IMAGE")
	if image == "" {
		t.Fatal("DP80_TEST_PG_IMAGE must name a locally available immutable image digest")
	}
	if out, err := exec.Command("docker", "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("fixed PostgreSQL image is not local (the test never pulls): %s: %v", out, err)
	}

	runID := fmt.Sprintf("dp80-%d-%d", os.Getpid(), time.Now().UnixNano())
	fixture := &mergeDBFixture{
		runID:       runID,
		source:      runID + "-source",
		destination: runID + "-destination",
		sourceDB:    "dp80_source",
		targetDB:    "dp80_target",
	}
	fixture.startContainer(t, image, fixture.source, fixture.sourceDB)
	fixture.startContainer(t, image, fixture.destination, fixture.targetDB)

	t.Run("preview is read only and happy path applies the exact plan", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		businessBefore := fixture.businessSnapshot(t)
		sequenceBefore := fixture.sequenceSnapshot(t)
		auditBefore := fixture.auditSnapshot(t)

		preview, _ := fixture.preview(t)
		if got := []int64{
			preview.FrameworksToInsert, preview.LabelsToInsert, preview.ArtistsToInsert,
			preview.SongsToInsert, preview.VariantsToBackfill,
			preview.PreservedSongDifferences, preview.Blockers,
			preview.ProductionSongsBefore, preview.ProductionSongsAfter,
		}; fmt.Sprint(got) != "[1 1 1 2 1 1 0 1 3]" {
			t.Fatalf("unexpected exact plan metrics: %v", got)
		}
		if fixture.businessSnapshot(t) != businessBefore || fixture.sequenceSnapshot(t) != sequenceBefore || fixture.auditSnapshot(t) != auditBefore {
			t.Fatal("preview changed business rows, audit rows, or sequences")
		}

		out, err := fixture.runScript("apply", preview.PreviewID)
		if err != nil {
			t.Fatalf("apply failed:\n%s\n%v", out, err)
		}
		for _, want := range []string{"source_check\",\"status\":\"completed", "plan_check\",\"status\":\"completed", "DP80_RESULT_JSON=", "result\",\"status\":\"committed"} {
			if !strings.Contains(out, want) {
				t.Fatalf("apply output omitted %q:\n%s", want, out)
			}
		}
		got := fixture.psql(t, fixture.destination, fixture.targetDB, `
SELECT (SELECT count(*) FROM frameworks),
       (SELECT count(*) FROM labels),
       (SELECT count(*) FROM artists),
       (SELECT count(*) FROM songs),
       (SELECT count(*) FROM songs WHERE variant_of_id IS NOT NULL),
       (SELECT count(*) FROM data_promotion_runs WHERE status='completed'),
       (SELECT result_json->>'frameworks_inserted' FROM data_promotion_runs LIMIT 1),
       (SELECT result_json->>'labels_inserted' FROM data_promotion_runs LIMIT 1),
       (SELECT result_json->>'artists_inserted' FROM data_promotion_runs LIMIT 1),
       (SELECT result_json->>'songs_inserted' FROM data_promotion_runs LIMIT 1),
       (SELECT result_json->>'variants_backfilled' FROM data_promotion_runs LIMIT 1),
       (SELECT result_json->>'preserved_song_differences' FROM data_promotion_runs LIMIT 1),
       (SELECT source_environment FROM data_promotion_runs LIMIT 1),
       (SELECT reviewer_name FROM data_promotion_runs LIMIT 1);`)
		if got != "2|2|2|3|1|1|1|1|1|2|1|1|8082|GeojoLu" {
			t.Fatalf("unexpected committed state: %q", got)
		}
		if _, err := os.Stat(filepath.Join(fixture.previewRoot, preview.PreviewID)); !os.IsNotExist(err) {
			t.Fatalf("successful one-time preview token still exists: %v", err)
		}
	})

	t.Run("discard consumes an unused token without database writes", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		businessBefore := fixture.businessSnapshot(t)
		sequenceBefore := fixture.sequenceSnapshot(t)
		auditBefore := fixture.auditSnapshot(t)
		preview, _ := fixture.preview(t)

		out, err := fixture.runScript("discard", preview.PreviewID)
		if err != nil || !strings.Contains(out, "discarded") {
			t.Fatalf("discard failed:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore || fixture.sequenceSnapshot(t) != sequenceBefore || fixture.auditSnapshot(t) != auditBefore {
			t.Fatal("discard changed business rows, sequences, or audit rows")
		}
		if _, err := os.Stat(filepath.Join(fixture.previewRoot, preview.PreviewID)); !os.IsNotExist(err) {
			t.Fatalf("discarded one-time preview token still exists: %v", err)
		}
	})

	t.Run("source drift is refused before audit or business writes", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		preview, _ := fixture.preview(t)
		businessBefore := fixture.businessSnapshot(t)
		fixture.psql(t, fixture.source, fixture.sourceDB, `UPDATE songs SET lyricist='changed after preview' WHERE name='Existing';`)

		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "source database drifted after preview") {
			t.Fatalf("source drift was not refused clearly:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore {
			t.Fatal("source-drift refusal changed destination business rows")
		}
		if got := fixture.auditSnapshot(t); got != "" {
			t.Fatalf("source-drift refusal wrote an audit before validation: %q", got)
		}
	})

	t.Run("target plan drift is refused and failed audit is durable", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		preview, _ := fixture.preview(t)
		fixture.psql(t, fixture.destination, fixture.targetDB, `
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist)
SELECT 'Parent',l.id,f.id,a.id,'target drift'
FROM labels l,frameworks f,artists a
WHERE l.name='ProdLabel' AND f.name='ProdFramework' AND a.name='A1';`)
		businessAfterDrift := fixture.businessSnapshot(t)

		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "approved preview drifted") {
			t.Fatalf("target drift was not refused clearly:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessAfterDrift {
			t.Fatal("target-drift refusal changed business rows beyond the injected drift")
		}
		if got := fixture.auditSnapshot(t); !strings.Contains(got, "|failed|") {
			t.Fatalf("target-drift failure did not leave a durable failed audit: %q", got)
		}
	})

	t.Run("late database failure rolls back every business table", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		preview, _ := fixture.preview(t)
		fixture.psql(t, fixture.destination, fixture.targetDB, `
CREATE FUNCTION dp80_reject_song() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture late insert failure'; END $$;
CREATE TRIGGER dp80_reject_song BEFORE INSERT ON songs
FOR EACH ROW EXECUTE FUNCTION dp80_reject_song();`)
		businessBefore := fixture.businessSnapshot(t)

		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "fixture late insert failure") || !strings.Contains(out, "rolled_back") {
			t.Fatalf("late failure did not report rollback:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore {
			t.Fatal("late failure did not roll back all business rows")
		}
		if got := fixture.auditSnapshot(t); !strings.Contains(got, "|failed|") {
			t.Fatalf("late failure did not leave a durable failed audit: %q", got)
		}
	})

	t.Run("concurrent apply consumes a preview exactly once", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		preview, _ := fixture.preview(t)
		fixture.psql(t, fixture.destination, fixture.targetDB, `
CREATE FUNCTION dp80_slow_song() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_sleep(1); RETURN NEW; END $$;
CREATE TRIGGER dp80_slow_song BEFORE INSERT ON songs
FOR EACH ROW EXECUTE FUNCTION dp80_slow_song();`)

		type result struct {
			out string
			err error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for range 2 {
			go func() {
				ready.Done()
				<-start
				out, err := fixture.runScript("apply", preview.PreviewID)
				results <- result{out: out, err: err}
			}()
		}
		ready.Wait()
		close(start)
		first, second := <-results, <-results
		successes := 0
		refusals := 0
		for _, result := range []result{first, second} {
			if result.err == nil && strings.Contains(result.out, `"status":"committed"`) {
				successes++
			} else if result.err != nil && strings.Contains(result.out, "already being applied or discarded") {
				refusals++
			}
		}
		if successes != 1 || refusals != 1 {
			t.Fatalf("want one commit and one lock refusal; got successes=%d refusals=%d\nfirst=%s\nsecond=%s", successes, refusals, first.out, second.out)
		}
		if got := fixture.psql(t, fixture.destination, fixture.targetDB, `
SELECT count(*),count(DISTINCT jsonb_build_array(s.name,a.name)::text),
       (SELECT count(*) FROM data_promotion_runs WHERE status='completed')
FROM songs s LEFT JOIN artists a ON a.id=s.artist_id;`); got != "3|3|1" {
			t.Fatalf("concurrent final state is not exactly once: %q", got)
		}
	})

	t.Run("blocker preview cannot be applied", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		fixture.psql(t, fixture.destination, fixture.targetDB, `
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist)
SELECT 'Existing',l.id,f.id,a.id,'duplicate'
FROM labels l,frameworks f,artists a
WHERE l.name='ProdLabel' AND f.name='ProdFramework' AND a.name='A1';`)
		preview, _ := fixture.preview(t)
		if preview.Blockers != 1 || preview.DuplicateProductionSongs != 1 {
			t.Fatalf("duplicate target key was not classified exactly: %+v", preview)
		}
		businessBefore := fixture.businessSnapshot(t)
		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "approved preview has 1 blocking conflict") {
			t.Fatalf("blocked plan was not refused:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore || fixture.auditSnapshot(t) != "" {
			t.Fatal("blocked plan changed business rows or audit state")
		}
	})

	t.Run("variant cycle is classified and cannot be applied", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		fixture.psql(t, fixture.source, fixture.sourceDB, `
UPDATE songs parent
SET variant_of_id = child.id
FROM songs child
WHERE parent.name='Parent' AND child.name='Child';`)
		preview, _ := fixture.preview(t)
		if preview.VariantCycles != 2 || preview.Blockers != 2 {
			t.Fatalf("variant cycle was not classified exactly: %+v", preview)
		}
		businessBefore := fixture.businessSnapshot(t)
		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "approved preview has 2 blocking conflict(s)") {
			t.Fatalf("variant-cycle plan was not refused:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore || fixture.auditSnapshot(t) != "" {
			t.Fatal("variant-cycle plan changed business rows or audit state")
		}
	})

	t.Run("missing destination sequence is classified and cannot be applied", func(t *testing.T) {
		fixture.reset(t)
		fixture.newPreviewRoot(t)
		fixture.psql(t, fixture.destination, fixture.targetDB, `
DROP SEQUENCE frameworks_id_seq CASCADE;`)
		preview, _ := fixture.preview(t)
		if preview.SequenceCacheBlockers != 1 || preview.Blockers != 1 {
			t.Fatalf("missing sequence configuration was not classified exactly: %+v", preview)
		}
		businessBefore := fixture.businessSnapshot(t)
		out, err := fixture.runScript("apply", preview.PreviewID)
		if err == nil || !strings.Contains(out, "approved preview has 1 blocking conflict(s)") {
			t.Fatalf("missing-sequence plan was not refused:\n%s\n%v", out, err)
		}
		if fixture.businessSnapshot(t) != businessBefore || fixture.auditSnapshot(t) != "" {
			t.Fatal("missing-sequence plan changed business rows or audit state")
		}
	})
}

func (f *mergeDBFixture) startContainer(t *testing.T, image, name, database string) {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		"--label", "dp80.test-fixture="+f.runID, "--network", "none",
		"-e", "POSTGRES_USER=datacenter", "-e", "POSTGRES_DB="+database,
		"-e", "POSTGRES_HOST_AUTH_METHOD=trust", image).CombinedOutput()
	if err != nil {
		t.Fatalf("start isolated PostgreSQL %s: %s: %v", name, out, err)
	}
	t.Cleanup(func() {
		label, inspectErr := exec.Command("docker", "inspect", "--format", `{{ index .Config.Labels "dp80.test-fixture" }}`, name).CombinedOutput()
		if inspectErr == nil && strings.TrimSpace(string(label)) == f.runID {
			_ = exec.Command("docker", "rm", "-f", name).Run()
		}
	})

	deadline := time.Now().Add(30 * time.Second)
	for delay := 50 * time.Millisecond; ; {
		if exec.Command("docker", "exec", name, "psql", "-X", "--no-password", "-qAt",
			"-U", "datacenter", "-d", database, "-c", "SELECT 1").Run() == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated PostgreSQL %s did not become ready", name)
		}
		time.Sleep(delay)
		if delay < time.Second {
			delay *= 2
		}
	}
}

func (f *mergeDBFixture) newPreviewRoot(t *testing.T) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "dp80-test-"+f.runID+"-")
	if err != nil {
		t.Fatal(err)
	}
	f.previewRoot = root
	t.Cleanup(func() { _ = os.RemoveAll(root) })
}

func (f *mergeDBFixture) reset(t *testing.T) {
	t.Helper()
	schema := `
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
CREATE TABLE frameworks (id serial PRIMARY KEY,name varchar NOT NULL,description varchar,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE labels (id serial PRIMARY KEY,name varchar NOT NULL);
CREATE TABLE artists (id serial PRIMARY KEY,name varchar NOT NULL,merged_into_id integer REFERENCES artists(id));
CREATE TABLE songs (
  id serial PRIMARY KEY,name varchar NOT NULL,label_id integer REFERENCES labels(id),
  framework_id integer REFERENCES frameworks(id),artist_id integer REFERENCES artists(id),
  lyricist varchar,composer varchar,upload_date date,release_date date,lyrics_url varchar,
  merged_into_id integer REFERENCES songs(id),variant_of_id integer REFERENCES songs(id));
CREATE TABLE data_promotion_runs (
  promotion_id varchar(96) PRIMARY KEY,
  source_environment varchar(32) NOT NULL DEFAULT '',source_database varchar(128) NOT NULL DEFAULT '',
  source_dump_sha256 varchar(64) NOT NULL DEFAULT '',source_backup_path text NOT NULL DEFAULT '',
  target_database varchar(128) NOT NULL DEFAULT '',operator_name varchar(64) NOT NULL DEFAULT '',
  reviewer_name varchar(64) NOT NULL DEFAULT '',scope_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  result_json jsonb,status varchar(24) NOT NULL DEFAULT 'running',started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,source_db text,target_db text,songs_inserted integer DEFAULT 0,
  artists_inserted integer DEFAULT 0,labels_inserted integer DEFAULT 0,collect_runs_inserted integer DEFAULT 0,
  users_synced integer DEFAULT 0,conflicts_json text,error_message text);
`
	f.psql(t, f.source, f.sourceDB, schema)
	f.psql(t, f.destination, f.targetDB, schema)
	f.psql(t, f.source, f.sourceDB, `
INSERT INTO frameworks(name) VALUES ('F1');
INSERT INTO labels(name) VALUES ('L1');
INSERT INTO artists(name) VALUES ('A1'),('A2');
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist,composer,upload_date,release_date,lyrics_url)
SELECT 'Existing',l.id,f.id,a.id,'source lyric','source composer','2026-01-01','2026-02-01','source-existing'
FROM labels l,frameworks f,artists a WHERE l.name='L1' AND f.name='F1' AND a.name='A1';
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist,composer,upload_date,release_date,lyrics_url)
SELECT 'Parent',l.id,f.id,a.id,'parent lyric','parent composer','2026-01-02','2026-02-02','source-parent'
FROM labels l,frameworks f,artists a WHERE l.name='L1' AND f.name='F1' AND a.name='A1';
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist,composer,upload_date,release_date,lyrics_url,variant_of_id)
SELECT 'Child',l.id,f.id,a.id,'child lyric','child composer','2026-01-03','2026-02-03','source-child',p.id
FROM labels l,frameworks f,artists a,songs p
WHERE l.name='L1' AND f.name='F1' AND a.name='A2' AND p.name='Parent';`)
	f.psql(t, f.destination, f.targetDB, `
INSERT INTO frameworks(name) VALUES ('ProdFramework');
INSERT INTO labels(name) VALUES ('ProdLabel');
INSERT INTO artists(name) VALUES ('A1');
INSERT INTO songs(name,label_id,framework_id,artist_id,lyricist,composer,upload_date,release_date,lyrics_url)
SELECT 'Existing',l.id,f.id,a.id,'production lyric','production composer','2025-01-01','2025-02-01','production-existing'
FROM labels l,frameworks f,artists a
WHERE l.name='ProdLabel' AND f.name='ProdFramework' AND a.name='A1';`)
}

func (f *mergeDBFixture) scriptEnv() []string {
	return append(os.Environ(),
		"DP80_TEST_FIXTURE=1", "DP80_TEST_RUN_ID="+f.runID,
		"DP80_TEST_SRC_CONTAINER="+f.source, "DP80_TEST_DST_CONTAINER="+f.destination,
		"DP80_TEST_SRC_DB="+f.sourceDB, "DP80_TEST_DST_DB="+f.targetDB,
		"DP80_TEST_PREVIEW_ROOT="+f.previewRoot)
}

func (f *mergeDBFixture) runScript(args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{"migrate-songs.sh"}, args...)...)
	cmd.Env = f.scriptEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *mergeDBFixture) preview(t *testing.T) (mergePreview, string) {
	t.Helper()
	out, err := f.runScript("preview")
	if err != nil {
		t.Fatalf("preview failed:\n%s\n%v", out, err)
	}
	preview, err := parseMergePreview([]byte(out))
	if err != nil {
		t.Fatalf("parse preview:\n%s\n%v", out, err)
	}
	return preview, out
}

func (f *mergeDBFixture) psql(t *testing.T, container, database, sql string) string {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", container, "psql", "-X", "--no-password", "-qAt", "-F", "|", "-v", "ON_ERROR_STOP=1", "-U", "datacenter", "-d", database)
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql %s/%s failed:\n%s\n%v", container, database, out, err)
	}
	return strings.TrimSpace(string(out))
}

func (f *mergeDBFixture) businessSnapshot(t *testing.T) string {
	t.Helper()
	return f.psql(t, f.destination, f.targetDB, `
SELECT jsonb_build_object(
 'frameworks',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM frameworks x),
 'labels',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM labels x),
 'artists',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM artists x),
 'songs',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM songs x))::text;`)
}

func (f *mergeDBFixture) sequenceSnapshot(t *testing.T) string {
	t.Helper()
	return f.psql(t, f.destination, f.targetDB, `
SELECT 'frameworks',last_value,is_called FROM frameworks_id_seq
UNION ALL SELECT 'labels',last_value,is_called FROM labels_id_seq
UNION ALL SELECT 'artists',last_value,is_called FROM artists_id_seq
UNION ALL SELECT 'songs',last_value,is_called FROM songs_id_seq ORDER BY 1;`)
}

func (f *mergeDBFixture) auditSnapshot(t *testing.T) string {
	t.Helper()
	return f.psql(t, f.destination, f.targetDB, `
SELECT promotion_id,status,coalesce(result_json::text,'') FROM data_promotion_runs ORDER BY promotion_id;`)
}
