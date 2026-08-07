package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

const appVersion = "1.1.0a"

// ── styles ────────────────────────────────────────────────────────────────────

// lipgloss Width(N) sets the box width *inside the border but including
// padding* — usable text width is N minus (2 × horizontal padding), and
// the fully rendered block (with border) is N+2 wide. The layout is no
// longer a fixed 80: it adapts to the terminal width (WindowSizeMsg) so the
// frame never exceeds the terminal and never wraps. defaultOuter is the
// ideal/only value used when no size is known yet (e.g. `dp80 status`).
const defaultOuter = 80

// layout derives the frame + pane dimensions from the terminal width.
//
//	outer — lipgloss Width() of the outer frame (rendered block is outer+2)
//	inner — usable text width inside the frame (outer-4, Padding(1,2))
//	pane  — lipgloss Width() of each side-by-side module box
//	stack — true when the terminal is too narrow for two panes side by side,
//	        so RELEASES / DATABASE stack vertically instead
//
// The DATABASE table is intrinsically ~31 columns; two of them side by side
// need inner ≥ 72 (each pane usable = (inner-10)/2 ≥ 31). Below that we stack.
func layout(term int) (outer, inner, pane int, stack bool) {
	if term <= 0 {
		term = defaultOuter + 2 // → outer == defaultOuter
	}
	outer = term - 2 // keep rendered block (outer+2) within the terminal
	if outer > 96 {
		outer = 96 // don't sprawl on ultra-wide terminals
	}
	if outer < 40 {
		outer = 40 // absolute floor
	}
	inner = outer - 4
	if inner >= 72 {
		if inner%2 != 0 { // need inner even so pane is an integer
			inner--
			outer--
		}
		// 2*(pane+2) + 2 == inner → pane = (inner-6)/2
		pane = (inner - 6) / 2
		stack = false
	} else {
		pane = inner - 2 // full-width stacked pane (rendered = inner)
		stack = true
	}
	return
}

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	sectionLabel = lipgloss.NewStyle().Bold(true)
	cmdKey       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cmdDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// outerStyle is the single frame around the entire TUI, every screen.
	// Its Width() is set per-render from the terminal size, so it is not
	// fixed here.
	outerStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")).
			Padding(1, 2)

	// pane styles: Width() is set per-render from layout(); only the
	// border/padding are fixed here.
	releasesPaneStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("6")).
				Padding(0, 1)

	databasePaneStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("5")).
				Padding(0, 1)

	paneTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// renderFramed wraps any screen's inner content in the single outer frame,
// sized to the given outer width.
func renderFramed(content string, outer int) string {
	return outerStyle.Width(outer).Render(content)
}

// lineCount returns the number of visual lines in s (ignoring a trailing "\n").
func lineCount(s string) int {
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}

// truncateLine clips s to at most max visible runes, adding "…" if clipped.
func truncateLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// ── SSH helpers ───────────────────────────────────────────────────────────────

const sshHost = "gandalf"

func sshRun(cmd string) string {
	out, _ := exec.Command("ssh", "-o", "BatchMode=yes", sshHost, cmd).Output()
	return strings.TrimSpace(string(out))
}

// ── data model ────────────────────────────────────────────────────────────────

type containerStatus struct {
	name  string
	state string
}

type boardData struct {
	containers     []containerStatus
	livezLocal     string
	livezPub       string
	beImage        string
	weImage        string
	songs80        string
	songs82        string
	artists80      string
	artists82      string
	pendingMerge   string // 8082 songs missing on :80 by content key (name+artist)
	collectState   string // "on (defaults)" | "on (N tasks)" | "off" | "?"
	gitHead        string
	currentCommit  string
	releases       []string
	releaseCommits []string
}

func fetchBoard() boardData {
	var d boardData

	raw := sshRun(`docker compose -p datacenter-kimi-production ps --format '{{.Service}} {{.State}} {{.Status}}' 2>/dev/null`)
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			d.containers = append(d.containers, containerStatus{parts[0], parts[1]})
		}
	}

	d.livezLocal = sshRun(`curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/api/livez 2>/dev/null`)
	d.livezPub = strings.TrimSpace(func() string {
		out, _ := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
			"https://gandalf.zebra-diminished.ts.net/api/livez").Output()
		return string(out)
	}())

	envRaw := sshRun(`cat /home/gandalf/projects/datacenter-kimi-production/release.env 2>/dev/null`)
	for _, l := range strings.Split(envRaw, "\n") {
		if strings.Contains(l, "BACKEND") {
			parts := strings.SplitN(l, "=", 2)
			if len(parts) == 2 && len(parts[1]) > 7 {
				d.beImage = parts[1][7:19]
			}
		}
		if strings.Contains(l, "WEB") {
			parts := strings.SplitN(l, "=", 2)
			if len(parts) == 2 && len(parts[1]) > 7 {
				d.weImage = parts[1][7:19]
			}
		}
	}

	data80 := strings.Split(sshRun(`docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null`), "\n")
	data82 := strings.Split(sshRun(`docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null`), "\n")
	if len(data80) >= 2 {
		d.songs80 = strings.TrimSpace(data80[0])
		d.artists80 = strings.TrimSpace(data80[1])
	}
	if len(data82) >= 2 {
		d.songs82 = strings.TrimSpace(data82[0])
		d.artists82 = strings.TrimSpace(data82[1])
	}

	d.collectState = fetchCollectState()

	// pending merge = 8082 songs missing on :80 by CONTENT key (name+artist).
	// Raw count diffs are meaningless here: the 2026-08-05 promote didn't
	// preserve ids and both sides collect independently, so 30k+ shared ids
	// hold different songs. Content keys are the only honest sync signal.
	d.pendingMerge = sshRun(`docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc "COPY (SELECT s.name||'|'||coalesce(a.name,'') FROM songs s LEFT JOIN artists a ON a.id=s.artist_id) TO STDOUT" 2>/dev/null | sort -u > /tmp/dp80_k80.txt
docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc "COPY (SELECT s.name||'|'||coalesce(a.name,'') FROM songs s LEFT JOIN artists a ON a.id=s.artist_id) TO STDOUT" 2>/dev/null | sort -u > /tmp/dp80_k82.txt
comm -13 /tmp/dp80_k80.txt /tmp/dp80_k82.txt | wc -l | tr -d ' '`)

	d.gitHead = sshRun(`git -C /home/gandalf/projects/datacenter-kimi-production log --oneline -1 2>/dev/null`)
	if parts := strings.Fields(d.gitHead); len(parts) > 0 {
		d.currentCommit = parts[0]
	}

	rels := sshRun(`ls -1t /home/gandalf/releases/datacenter-kimi/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5`)
	for _, r := range strings.Split(rels, "\n") {
		if r != "" {
			d.releases = append(d.releases, r)
		}
	}
	for _, rid := range d.releases {
		d.releaseCommits = append(d.releaseCommits, relCommit(rid))
	}

	return d
}

// fetchCollectState reads :80's collect strategy (app_config['collect_strategy']).
// No stored row means the scheduler runs on code DEFAULTS (song collection ON
// daily for every platform) — absence is "on", not "off".
func fetchCollectState() string {
	raw := sshRun(`docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc "SELECT value FROM app_config WHERE key='collect_strategy'" 2>/dev/null`)
	if strings.TrimSpace(raw) == "" {
		return "on (defaults)"
	}
	var stored map[string]map[string]struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal([]byte(raw), &stored) != nil {
		return "?"
	}
	n := 0
	for _, tasks := range stored {
		for _, t := range tasks {
			if t.Enabled {
				n++
			}
		}
	}
	if n == 0 {
		return "off"
	}
	return fmt.Sprintf("on (%s tasks)", formatExact(int64(n)))
}

// setCollect80 flips :80 collection. Off = write an explicit all-disabled
// strategy (every platform × song_info/comment/discovery) + artist fans off,
// and abort any running collect_runs rows. On = delete the stored rows so the
// scheduler falls back to code defaults. The scheduler listens on a DB event
// and re-reads the strategy immediately (verified 2026-08-06 10:16).
func setCollect80(turnOff bool) tea.Cmd {
	return func() tea.Msg {
		var sql string
		if turnOff {
			sql = `INSERT INTO app_config (key, value) VALUES ('collect_strategy',
  '{"qq_music": {"song_info": {"enabled": false}, "comment": {"enabled": false}, "discovery": {"enabled": false}},
    "kuwo":     {"song_info": {"enabled": false}, "comment": {"enabled": false}, "discovery": {"enabled": false}},
    "kugou":    {"song_info": {"enabled": false}, "comment": {"enabled": false}, "discovery": {"enabled": false}},
    "netease":  {"song_info": {"enabled": false}, "comment": {"enabled": false}, "discovery": {"enabled": false}},
    "soda":     {"song_info": {"enabled": false}, "comment": {"enabled": false}, "discovery": {"enabled": false}}}'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
INSERT INTO app_config (key, value) VALUES ('artist_fans_strategy', '{"enabled": false}'::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
UPDATE collect_runs SET status='aborted' WHERE status='running';`
		} else {
			sql = `DELETE FROM app_config WHERE key IN ('collect_strategy', 'artist_fans_strategy');`
		}
		cmd := exec.Command("ssh", "-o", "BatchMode=yes", sshHost,
			"docker exec -i datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -v ON_ERROR_STOP=1")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return collectError(string(out) + fmt.Sprint(err))
		}
		return collectDone{}
	}
}

// killCollect80 aborts all running collect_runs WITHOUT touching strategy.
// Use when strategy is on but you want to temporarily stop current runs, or
// when cleaning up orphaned running rows (2026-08-06).
func killCollect80() tea.Cmd {
	return func() tea.Msg {
		sql := `UPDATE collect_runs SET status='aborted' WHERE status='running';`
		cmd := exec.Command("ssh", "-o", "BatchMode=yes", sshHost,
			"docker exec -i datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -v ON_ERROR_STOP=1")
		cmd.Stdin = strings.NewReader(sql)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return collectError(string(out) + fmt.Sprint(err))
		}
		return collectDone{}
	}
}

// fetchCollectRunning reads how many collect_runs are status='running'.
func fetchCollectRunning() string {
	raw := sshRun(`docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc "SELECT count(*) FROM collect_runs WHERE status='running'" 2>/dev/null`)
	return formatExactString(raw)
}

func relCommit(rid string) string {
	cmd := fmt.Sprintf(`[ -f /home/gandalf/releases/datacenter-kimi/%s/release.json ] && python3 -c 'import json; m=json.load(open("/home/gandalf/releases/datacenter-kimi/%s/release.json")); print(m.get("release",{}).get("commit","?")[:8])' 2>/dev/null || echo '--'`, rid, rid)
	return sshRun(cmd)
}

func fetchAuditLog() string {
	// 获取迁移审计历史
	auditCmd := `docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc "
SELECT
  promotion_id,
  TO_CHAR(started_at, 'MM-DD HH:MI:SS') as time,
  songs_inserted,
  status
FROM data_promotion_runs
WHERE promotion_id LIKE 'migrate-%'
ORDER BY started_at DESC
LIMIT 10;" 2>/dev/null`
	return sshRun(auditCmd)
}

// releaseManifest is the subset of release.json dp80 needs to show and apply.
type releaseManifest struct {
	path      string
	releaseID string
	commit    string
	beVersion string
	weVersion string
	webImage  string // full sha256 image ID, for the pre-apply content gate
	source    string // "local"
}

// localRepo is the canonical datacenter-kimi checkout on this machine
// (kenPro). production_80.py has LOCAL_REPO hardcoded to this path and is
// designed to be *orchestrated from here*: apply runs locally and the
// script itself ssh's to gandalf to push. So dp80 reads the prepared
// manifest here and runs apply here — not on gandalf.
const localRepo = "/Users/geojol/Documents/Projects/datacenter-kimi"
const manifestRelDir = "release-manifest/production-80"

// findLatestManifest finds the newest prepared release manifest in the local
// datacenter-kimi checkout (IDs are UTC-timestamps, lexicographic =
// chronological, so the last entry is newest).
func findLatestManifest() (releaseManifest, error) {
	var m releaseManifest

	dir := localRepo + "/" + manifestRelDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, fmt.Errorf("cannot read %s: %w", dir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return m, fmt.Errorf("no prepared manifest — run prepare first")
	}
	sort.Strings(ids) // lexicographic == chronological
	latest := ids[len(ids)-1]

	m.path = dir + "/" + latest + "/release.json"

	rawBytes, err := os.ReadFile(m.path)
	if err != nil {
		return m, fmt.Errorf("cannot read %s: %w", m.path, err)
	}
	raw := string(rawBytes)
	var j struct {
		ReleaseID string `json:"release_id"`
		Release   struct {
			Commit         string `json:"commit"`
			BackendVersion string `json:"backend_version"`
			WebVersion     string `json:"web_version"`
			WebImage       string `json:"web_image"`
		} `json:"release"`
	}
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return m, fmt.Errorf("parse %s: %w", m.path, err)
	}
	m.releaseID = j.ReleaseID
	m.commit = j.Release.Commit
	m.beVersion = j.Release.BackendVersion
	m.weVersion = j.Release.WebVersion
	m.webImage = j.Release.WebImage
	m.source = "local"
	return m, nil
}

func fetchBackendLogsClean() string {
	// 获取 backend 日志，过滤掉 livez 噪音；如果全是 livez，显示最近的审计摘要
	logsCmd := `docker compose -p datacenter-kimi-production logs --tail=200 backend 2>/dev/null | grep -v 'livez\|GET /api/health' | tail -30 || echo '  (最近都是健康检查日志，查看迁移历史了解最近的活动)'`
	return sshRun(logsCmd)
}

// ── model ─────────────────────────────────────────────────────────────────────

type screen int

const (
	screenBoard screen = iota
	screenMergePreviewing
	screenConfirmMerge
	screenMerging
	screenConfirmDeploy
	screenDeploying
	screenConfirmCollect
	screenConfirmKill
	screenLog
)

type model struct {
	board       boardData
	loading     bool
	termWidth   int // last known terminal width (0 = unknown yet)
	screen      screen
	logContent  string // screenMerging/screenDeploying: raw script output
	auditText   string // screenLog: raw psql audit rows
	backendLogs string // screenLog: raw filtered backend logs
	manifest    releaseManifest
	manifestErr string
	err         string

	// deploy progress state (screenDeploying)
	deployStart  time.Time
	phases       []ledgerPhase // committed rows from one long-lived ledger stream
	deployEvents <-chan deployStreamItem
	gateState    string // "" | "checking" | "passed" | "failed"
	liveGate     string // "" | "checking" | "passed" | "failed"
	spin         int    // local-only spinner frame counter

	// merge progress state (screenMerging)
	mergeStart            time.Time
	mergePreview          mergePreview
	mergePreviewPage      int
	mergeJob              mergeJob
	mergeEvents           <-chan mergeStreamItem
	mergePhase            string
	mergeStatus           string
	mergeTerminal         bool
	mergeExitCode         *int
	mergeLog              string
	mergePreviewRequestID string
	mergePreviewCancel    context.CancelFunc
}

// mergePreview is the exact immutable plan produced by migrate-songs.sh.
// Counts are int64 so exact database values are never rounded or abbreviated.
type mergePreview struct {
	SchemaVersion                 int    `json:"schema_version"`
	PreviewID                     string `json:"preview_id"`
	SourceDigest                  string `json:"source_digest"`
	PlanDigest                    string `json:"plan_digest"`
	SourceSongRows                int64  `json:"source_song_rows"`
	UniqueSongKeys                int64  `json:"unique_song_keys"`
	DuplicateSourceRows           int64  `json:"duplicate_source_rows"`
	DuplicateSourceSongKeys       int64  `json:"duplicate_source_song_keys"`
	SourceArtistRows              int64  `json:"source_artist_rows"`
	UniqueArtists                 int64  `json:"unique_artists"`
	FrameworksToInsert            int64  `json:"frameworks_to_insert"`
	LabelsToInsert                int64  `json:"labels_to_insert"`
	ArtistsToInsert               int64  `json:"artists_to_insert"`
	SongsToInsert                 int64  `json:"songs_to_insert"`
	VariantsToBackfill            int64  `json:"variants_to_backfill"`
	VariantConflicts              int64  `json:"variant_conflicts"`
	InvalidReleaseDates           int64  `json:"invalid_release_dates"`
	DuplicateProductionSongs      int64  `json:"duplicate_production_song_keys"`
	DuplicateProductionArtists    int64  `json:"duplicate_production_artist_names"`
	DuplicateProductionLabels     int64  `json:"duplicate_production_label_names"`
	DuplicateProductionFrameworks int64  `json:"duplicate_production_framework_names"`
	MissingVariantTargets         int64  `json:"missing_variant_targets"`
	SelfVariantLinks              int64  `json:"self_variant_links"`
	SequenceCacheBlockers         int64  `json:"sequence_cache_blockers"`
	VariantCycles                 int64  `json:"variant_cycles"`
	PreservedSongDifferences      int64  `json:"preserved_song_differences"`
	Blockers                      int64  `json:"blockers"`
	ProductionSongsBefore         int64  `json:"production_songs_before"`
	ProductionSongsAfter          int64  `json:"production_songs_after"`
	SequenceBumps                 int64  `json:"sequence_bumps"`
}

type mergeJob struct {
	ID        string
	PID       string
	RemoteDir string
}

type mergeEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Phase         string `json:"phase"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

type mergeStreamItem struct {
	line     string
	event    *mergeEvent
	exitCode *int
	err      error
}

type deployStreamItem struct {
	phase  *ledgerPhase
	output string
	err    error
	done   bool
}

// ledgerPhase is one committed line from the append-only release ledger.
type ledgerPhase struct {
	at     string
	phase  string
	status string
}

// applyTotalPhases is how many ledger phases a full successful apply writes
// (PREFLIGHT → RUNTIME_VERIFIED). Used only to scale the progress bar; the
// phase names themselves are displayed straight from the ledger.
const applyTotalPhases = 11

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type boardLoaded boardData
type mergePreviewLoaded struct {
	requestID string
	preview   mergePreview
}
type mergePreviewError struct {
	requestID string
	message   string
}
type mergePreviewDiscarded struct{}
type mergePreviewDiscardError string
type mergeJobLaunched mergeJob
type mergeStreamReady struct{ ch <-chan mergeStreamItem }
type mergeStreamMsg mergeStreamItem
type mergeError string
type collectDone struct{}
type collectError string
type deployDone string
type deployError string
type gateResult struct {
	ok   bool
	info string
}
type liveGateResult bool
type deployStreamReady struct{ ch <-chan deployStreamItem }
type deployStreamMsg deployStreamItem
type deployTick time.Time

func loadBoard() tea.Msg {
	d := fetchBoard()
	return boardLoaded(d)
}

const previewPrefix = "DP80_PREVIEW_JSON="
const eventPrefix = "DP80_EVENT_JSON="
const exitPrefix = "DP80_EXIT_CODE="

func parseMergePreview(out []byte) (mergePreview, error) {
	var preview mergePreview
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, previewPrefix) {
			continue
		}
		if found {
			return preview, fmt.Errorf("preview output contains more than one terminal record")
		}
		raw := strings.TrimPrefix(line, previewPrefix)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return preview, fmt.Errorf("invalid preview JSON: %w", err)
		}
		required := []string{
			"schema_version", "preview_id", "source_digest", "plan_digest",
			"source_song_rows", "unique_song_keys", "duplicate_source_rows", "duplicate_source_song_keys",
			"source_artist_rows", "unique_artists", "frameworks_to_insert",
			"labels_to_insert", "artists_to_insert", "songs_to_insert",
			"variants_to_backfill", "variant_conflicts", "invalid_release_dates",
			"duplicate_production_song_keys", "duplicate_production_artist_names",
			"duplicate_production_label_names", "duplicate_production_framework_names",
			"missing_variant_targets", "self_variant_links", "sequence_cache_blockers",
			"variant_cycles",
			"preserved_song_differences", "blockers", "production_songs_before",
			"production_songs_after", "sequence_bumps",
		}
		for _, name := range required {
			if _, ok := fields[name]; !ok {
				return preview, fmt.Errorf("preview JSON is missing %s", name)
			}
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&preview); err != nil {
			return preview, fmt.Errorf("invalid preview JSON: %w", err)
		}
		found = true
	}
	if !found {
		return preview, fmt.Errorf("preview output has no terminal record")
	}
	if err := validateMergePreview(preview); err != nil {
		return preview, err
	}
	return preview, nil
}

func validateMergePreview(p mergePreview) error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported preview schema_version %d", p.SchemaVersion)
	}
	if !validRemoteName(p.PreviewID) || !strings.HasPrefix(p.PreviewID, "preview-") {
		return fmt.Errorf("invalid preview_id")
	}
	if len(p.SourceDigest) != 64 || len(p.PlanDigest) != 64 {
		return fmt.Errorf("invalid preview digest")
	}
	for _, digest := range []string{p.SourceDigest, p.PlanDigest} {
		if _, err := hex.DecodeString(digest); err != nil {
			return fmt.Errorf("invalid preview digest: %w", err)
		}
	}
	counts := []int64{
		p.SourceSongRows, p.UniqueSongKeys, p.DuplicateSourceRows, p.DuplicateSourceSongKeys,
		p.SourceArtistRows, p.UniqueArtists, p.FrameworksToInsert,
		p.LabelsToInsert, p.ArtistsToInsert, p.SongsToInsert,
		p.VariantsToBackfill, p.VariantConflicts, p.InvalidReleaseDates,
		p.DuplicateProductionSongs, p.DuplicateProductionArtists,
		p.DuplicateProductionLabels, p.DuplicateProductionFrameworks,
		p.MissingVariantTargets, p.SelfVariantLinks, p.SequenceCacheBlockers,
		p.VariantCycles,
		p.PreservedSongDifferences, p.Blockers, p.ProductionSongsBefore,
		p.ProductionSongsAfter, p.SequenceBumps,
	}
	for _, n := range counts {
		if n < 0 {
			return fmt.Errorf("preview contains a negative exact count")
		}
	}
	if p.ProductionSongsBefore+p.SongsToInsert != p.ProductionSongsAfter {
		return fmt.Errorf("preview production song totals do not reconcile")
	}
	classifiedBlockers := p.DuplicateSourceSongKeys + p.DuplicateProductionSongs +
		p.DuplicateProductionArtists + p.DuplicateProductionLabels +
		p.DuplicateProductionFrameworks + p.MissingVariantTargets +
		p.SelfVariantLinks + p.VariantConflicts + p.InvalidReleaseDates +
		p.SequenceCacheBlockers + p.VariantCycles
	if classifiedBlockers != p.Blockers {
		return fmt.Errorf("preview blocker categories do not reconcile")
	}
	return nil
}

func formatExact(n int64) string {
	s := strconv.FormatInt(n, 10)
	start := 0
	if strings.HasPrefix(s, "-") {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func parseExactString(raw string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n, err == nil && n >= 0
}

func formatExactString(raw string) string {
	if n, ok := parseExactString(raw); ok {
		return formatExact(n)
	}
	return "?"
}

func validRemoteName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// ── render helpers ────────────────────────────────────────────────────────────

// colorStatus reduces a verbose docker status ("Up 15 hours (healthy)") to
// a short, uniform-length word so STATUS rows don't end ragged.
func colorStatus(state string) string {
	switch {
	case strings.Contains(state, "healthy"):
		return green.Render("● healthy")
	case strings.Contains(state, "Up"):
		return green.Render("● running")
	case state == "":
		return dim.Render("○ unknown")
	default:
		return red.Render("● down")
	}
}

func colorHTTP(code string) string {
	if code == "200" {
		return bold.Foreground(lipgloss.Color("2")).Render(code)
	}
	if code == "" {
		return dim.Render("?")
	}
	return bold.Foreground(lipgloss.Color("1")).Render(code)
}

func colorDiff(a, b string) string {
	ai, aOK := parseExactString(a)
	bi, bOK := parseExactString(b)
	if !aOK || !bOK {
		return dim.Render("?")
	}
	d := bi - ai
	switch {
	case d > 0:
		return yellow.Bold(true).Render("+" + formatExact(d))
	case d < 0:
		return red.Bold(true).Render(formatExact(d))
	default:
		return dim.Render("0")
	}
}

// dims returns the current layout dimensions from the last known terminal
// width (falls back to the default 80-wide frame when size is unknown).
func (m model) dims() (outer, inner, pane int, stack bool) {
	return layout(m.termWidth)
}

func (m model) renderReleasesPane() string {
	d := m.board
	var sb strings.Builder
	sb.WriteString(paneTitle.Render("RELEASES") + "\n")
	sb.WriteString(fmt.Sprintf("%-8s %s\n", "live", green.Render(d.currentCommit)))

	if len(d.releases) == 0 {
		sb.WriteString(dim.Render("(none)") + "\n")
		return sb.String()
	}

	for i, r := range d.releases {
		commit := ""
		if i < len(d.releaseCommits) {
			commit = d.releaseCommits[i]
		}
		if commit == "" || commit == "--" {
			commit = "--------"
		}
		isLive := commit != "--------" && d.currentCommit != "" && strings.HasPrefix(d.currentCommit, commit)

		// 提取时间戳：20260803T074303Z → "08-03"
		timeStr := r
		if len(r) >= 8 {
			timeStr = r[4:6] + "-" + r[6:8]
		}

		// 先对纯文本定宽，再统一上色，避免 ANSI 转义码干扰 %s 宽度计算
		label := fmt.Sprintf("%-8s %-5s", commit, timeStr)
		if isLive {
			sb.WriteString(green.Bold(true).Render("●") + " " + green.Render(label+" live") + "\n")
		} else {
			sb.WriteString(dim.Render("○") + " " + dim.Render(label) + "\n")
		}
	}
	return sb.String()
}

func (m model) renderDatabasePane() string {
	d := m.board
	var sb strings.Builder
	sb.WriteString(paneTitle.Render("DATABASE") + "\n")
	sb.WriteString(dim.Render("prod :80 vs test :8082") + "\n")

	// 表格：每行用同一个格式串（含空标签的表头行），保证列对齐；数值先在
	// 纯文本上定宽 padNum 再整体上色。注意：colored Render() 绝不能喂入带
	// 尾部 \n 的字符串——lipgloss 会把换行后的"空尾段"补齐到上一行等宽，
	// 这些空格会串到下一行开头，把表格撑得错位换行。所以 \n 一律在
	// Render() 之外单独拼接。
	padNum := func(v string) string { return fmt.Sprintf("%8s", v) }
	row := func(label, v80, v82, diff string) string {
		return fmt.Sprintf("%-7s %s %s  %s", label, v80, v82, diff)
	}
	sb.WriteString(dim.Render(row("", padNum(":80"), padNum(":8082"), "diff")) + "\n")
	sb.WriteString(row("songs", bold.Render(padNum(formatExactString(d.songs80))), padNum(formatExactString(d.songs82)), colorDiff(d.songs80, d.songs82)) + "\n")
	sb.WriteString(row("artists", bold.Render(padNum(formatExactString(d.artists80))), padNum(formatExactString(d.artists82)), colorDiff(d.artists80, d.artists82)) + "\n")
	sb.WriteString("\n")

	// sync status by content key, not by count diff — counts can't agree
	// while the two sides' id spaces are diverged (:80 keeps its own extra
	// collected songs on top of everything from 8082).
	pending, pendingKnown := parseExactString(d.pendingMerge)
	switch {
	case pendingKnown && pending > 0:
		sb.WriteString(yellow.Render("⚠ "+formatExact(pending)+" songs pending") + "\n")
		sb.WriteString(dim.Render("press m to merge") + "\n")
	case pendingKnown && pending == 0:
		sb.WriteString(green.Render("✓ synced (content)") + "\n")
	default:
		sb.WriteString(dim.Render("? sync status unknown") + "\n")
	}
	return sb.String()
}

// renderBoardContent builds the inner content of the board screen — the
// outer frame itself is applied once, uniformly, in View().
func (m model) renderBoardContent() string {
	var sb strings.Builder
	d := m.board

	sb.WriteString(titleStyle.Render("Jose · :80 Deploy Panel") + "\n\n")

	// STATUS：一行一项，label 定宽对齐，不同信息不挤同一行
	sb.WriteString(sectionLabel.Render("STATUS") + "\n")
	cmap := map[string]string{}
	for _, c := range d.containers {
		cmap[c.name] = c.state
	}
	for _, name := range []string{"backend", "scheduler", "proxy", "db"} {
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", name, colorStatus(cmap[name])))
	}
	sb.WriteString(fmt.Sprintf("  %-16s %s\n", "livez local", colorHTTP(d.livezLocal)))
	sb.WriteString(fmt.Sprintf("  %-16s %s\n", "livez public", colorHTTP(d.livezPub)))
	sb.WriteString(fmt.Sprintf("  %-16s %s\n", "image backend", dim.Render(d.beImage)))
	sb.WriteString(fmt.Sprintf("  %-16s %s\n", "image web", dim.Render(d.weImage)))
	// :80 生产不该自己采集(业务数据只从 8082 merge 过来)——on 是黄色警示
	switch {
	case d.collectState == "off":
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", "collect :80", dim.Render("○ off")))
	case strings.HasPrefix(d.collectState, "on"):
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", "collect :80", yellow.Render("● "+d.collectState)))
	default:
		sb.WriteString(fmt.Sprintf("  %-16s %s\n", "collect :80", dim.Render("? unknown")))
	}
	if m.err != "" {
		sb.WriteString("  " + red.Render(truncateLine(m.err, 70)) + "\n")
	}
	sb.WriteString("\n")

	// RELEASES + DATABASE. Wide terminals show them side by side, both
	// boxes the same height so neither is longer; narrow terminals stack
	// them vertically so nothing overflows or wraps.
	_, inner, pane, stack := m.dims()
	left := strings.TrimRight(m.renderReleasesPane(), "\n")
	right := strings.TrimRight(m.renderDatabasePane(), "\n")
	if stack {
		// two full-width boxes, one above the other
		leftBox := releasesPaneStyle.Width(pane).Render(left)
		rightBox := databasePaneStyle.Width(pane).Render(right)
		sb.WriteString(leftBox + "\n" + rightBox + "\n\n")
	} else {
		// 两个面板内容都以 "\n" 结尾——必须先剪掉，否则 lipgloss 会把换行后
		// 的空尾段当成真实一行渲染，实际高度比 lineCount() 多算 1 行，且两边
		// 多出来的量还可能不一样，Height() 对齐就失效了。
		h := max(lineCount(left), lineCount(right))
		leftBox := releasesPaneStyle.Width(pane).Height(h).Render(left)
		rightBox := databasePaneStyle.Width(pane).Height(h).Render(right)
		sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftBox, "  ", rightBox) + "\n\n")
	}

	// Command bar
	sb.WriteString(dim.Render(strings.Repeat("─", inner)) + "\n")
	sb.WriteString(
		cmdKey.Render("d") + cmdDim.Render(")eploy  ") +
			cmdKey.Render("r") + cmdDim.Render(")efresh  ") +
			cmdKey.Render("m") + cmdDim.Render(")erge  ") +
			cmdKey.Render("c") + cmdDim.Render(")ollect  ") +
			cmdKey.Render("k") + cmdDim.Render(")ill  ") +
			cmdKey.Render("l") + cmdDim.Render(")ogs  ") +
			cmdKey.Render("q") + cmdDim.Render(")uit"))

	return sb.String()
}

// ── init / update / view ──────────────────────────────────────────────────────

func (m model) Init() tea.Cmd {
	return tea.Batch(tea.ClearScrollArea, loadBoard)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		return m, nil

	case boardLoaded:
		m.board = boardData(msg)
		m.loading = false
		return m, nil

	case tea.KeyMsg:
		switch m.screen {
		case screenBoard:
			switch msg.String() {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "r":
				m.loading = true
				return m, loadBoard
			case "d":
				mf, err := findLatestManifest()
				m.manifest = mf
				if err != nil {
					m.manifestErr = err.Error()
				} else {
					m.manifestErr = ""
				}
				m.screen = screenConfirmDeploy
				return m, nil
			case "m":
				requestID, err := newJobID("preview-request")
				if err != nil {
					m.err = "cannot create preview request: " + err.Error()
					return m, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				m.screen = screenMergePreviewing
				m.logContent = ""
				m.err = ""
				m.mergePreview = mergePreview{}
				m.mergePreviewRequestID = requestID
				m.mergePreviewCancel = cancel
				m.mergeStart = time.Now()
				m.spin = 0
				return m, tea.Batch(runMergePreview(ctx, requestID), tickDeploy())
			case "c":
				m.screen = screenConfirmCollect
				return m, nil
			case "k":
				m.screen = screenConfirmKill
				return m, nil
			case "l":
				m.screen = screenLog
				m.auditText = fetchAuditLog()
				m.backendLogs = fetchBackendLogsClean()
				return m, nil
			}
		case screenMergePreviewing:
			switch msg.String() {
			case "esc", "q":
				if m.mergePreviewCancel != nil {
					m.mergePreviewCancel()
				}
				m.mergePreviewCancel = nil
				m.mergePreviewRequestID = ""
				m.screen = screenBoard
				return m, nil
			}
		case screenConfirmMerge:
			switch msg.String() {
			case "tab", "right":
				m.mergePreviewPage = (m.mergePreviewPage + 1) % 3
				return m, nil
			case "shift+tab", "left":
				m.mergePreviewPage = (m.mergePreviewPage + 2) % 3
				return m, nil
			case "M":
				if m.mergePreviewPage != 0 || m.mergePreview.Blockers > 0 {
					return m, nil
				}
				m.screen = screenMerging
				m.logContent = ""
				m.mergeStart = time.Now()
				m.mergeJob = mergeJob{}
				m.mergeEvents = nil
				m.mergePhase = "launch"
				m.mergeStatus = "running"
				m.mergeTerminal = false
				m.mergeExitCode = nil
				m.mergeLog = ""
				m.mergePreviewCancel = nil
				m.mergePreviewRequestID = ""
				return m, tea.Batch(launchMerge(m.mergePreview), tickDeploy())
			case "esc", "q", "n", "N", "enter":
				previewID := m.mergePreview.PreviewID
				m.mergePreview = mergePreview{}
				m.screen = screenBoard
				return m, discardMergePreview(previewID)
			}
		case screenConfirmCollect:
			switch msg.String() {
			case "y", "Y":
				m.loading = true
				m.screen = screenBoard
				return m, setCollect80(m.board.collectState != "off")
			case "esc", "q", "n", "N", "enter":
				m.screen = screenBoard
				return m, nil
			}
		case screenConfirmKill:
			switch msg.String() {
			case "y", "Y":
				m.loading = true
				m.screen = screenBoard
				return m, killCollect80()
			case "esc", "q", "n", "N", "enter":
				m.screen = screenBoard
				return m, nil
			}
		case screenConfirmDeploy:
			switch msg.String() {
			case "y", "Y":
				if m.manifestErr != "" {
					m.screen = screenBoard
					return m, nil
				}
				m.screen = screenDeploying
				m.logContent = ""
				m.deployStart = time.Now()
				m.phases = nil
				m.gateState = "checking"
				m.liveGate = ""
				return m, tea.Batch(checkContentGate(m.manifest.webImage), tickDeploy())
			case "esc", "q", "n", "N", "enter":
				m.screen = screenBoard
				return m, nil
			}
		case screenMerging, screenDeploying:
			switch msg.String() {
			case "q", "esc", "enter":
				m.screen = screenBoard
				m.loading = true
				return m, loadBoard
			}
		case screenLog:
			switch msg.String() {
			case "q", "esc", "enter":
				m.screen = screenBoard
				return m, nil
			}
		}

	case mergePreviewLoaded:
		if m.screen != screenMergePreviewing || msg.requestID != m.mergePreviewRequestID {
			// A cancelled request can race with its already-completed SSH command.
			// Never let that late message reopen the UI or leak its one-time token.
			return m, discardMergePreview(msg.preview.PreviewID)
		}
		m.mergePreview = msg.preview
		m.mergePreviewPage = 0
		m.mergePreviewRequestID = ""
		m.mergePreviewCancel = nil
		m.screen = screenConfirmMerge
		return m, nil

	case mergePreviewError:
		if m.screen != screenMergePreviewing || msg.requestID != m.mergePreviewRequestID {
			return m, nil
		}
		m.mergePreviewRequestID = ""
		m.mergePreviewCancel = nil
		m.logContent = red.Render("Preview failed") + "\n\n" + msg.message
		return m, nil

	case mergePreviewDiscarded:
		return m, nil

	case mergePreviewDiscardError:
		m.err = "preview cleanup failed: " + string(msg)
		return m, nil

	case mergeJobLaunched:
		m.mergeJob = mergeJob(msg)
		m.mergePhase = "attach"
		return m, startMergeStream(m.mergeJob)

	case mergeStreamReady:
		m.mergeEvents = msg.ch
		return m, waitMergeStream(m.mergeEvents)

	case mergeStreamMsg:
		item := mergeStreamItem(msg)
		if item.err != nil {
			if !m.mergeTerminal {
				m.mergeStatus = "stream disconnected"
				m.mergeLog = appendMergeLog(m.mergeLog, "[STREAM] "+item.err.Error())
			}
			return m, nil
		}
		if item.event != nil {
			if item.event.SchemaVersion != 1 {
				m.mergeLog = appendMergeLog(m.mergeLog, "[EVENT] unsupported schema version")
			} else {
				m.mergePhase = item.event.Phase
				m.mergeStatus = item.event.Status
				m.mergeLog = appendMergeLog(m.mergeLog,
					fmt.Sprintf("[%s] %s: %s", item.event.Status, item.event.Phase, item.event.Message))
			}
		} else if item.exitCode != nil {
			rc := *item.exitCode
			m.mergeExitCode = &rc
			m.mergeTerminal = true
			if rc == 0 {
				m.mergeStatus = "committed"
			} else if m.mergeStatus != "rolled_back" && m.mergeStatus != "refused" {
				m.mergeStatus = "failed"
			}
		} else if item.line != "" && !strings.HasPrefix(item.line, "DP80_RESULT_JSON=") {
			m.mergeLog = appendMergeLog(m.mergeLog, item.line)
		}
		if !m.mergeTerminal && m.mergeEvents != nil {
			return m, waitMergeStream(m.mergeEvents)
		}
		return m, nil

	case mergeError:
		m.mergeStatus = "failed"
		m.mergeTerminal = true
		m.mergeLog = appendMergeLog(m.mergeLog, "Error: "+string(msg))
		if m.mergeJob.ID == "" && m.mergePreview.PreviewID != "" {
			return m, discardMergePreview(m.mergePreview.PreviewID)
		}
		return m, nil

	case collectDone:
		m.loading = true
		return m, loadBoard

	case collectError:
		m.err = string(msg)
		m.loading = true
		return m, loadBoard

	case gateResult:
		if !msg.ok {
			m.gateState = "failed"
			m.logContent = red.Render("REFUSED by content gate") + "\n\n" +
				yellow.Render("候选 web 镜像的 Explore chunk 缺「歌曲使用量」——构建时丢了") + "\n" +
				yellow.Render("--build-arg VITE_SONG_USAGE_ENABLED=true,拒绝部署。") + "\n\n" +
				"用 scripts/build-release-images.sh 重建镜像后重新 prepare。\n" +
				dim.Render("(gate output: "+strings.TrimSpace(msg.info)+")")
			return m, nil
		}
		m.gateState = "passed"
		return m, startDeployStream(m.manifest.path, m.manifest.releaseID)

	case deployStreamReady:
		m.deployEvents = msg.ch
		return m, waitDeployStream(m.deployEvents)

	case deployStreamMsg:
		item := deployStreamItem(msg)
		if item.phase != nil {
			m.phases = append(m.phases, *item.phase)
		}
		if item.done {
			if item.err != nil {
				return updateDeployFailure(m, item.output+"\n"+item.err.Error())
			}
			m.logContent = item.output
			m.liveGate = "checking"
			return m, checkLiveGate
		}
		if m.deployEvents != nil {
			return m, waitDeployStream(m.deployEvents)
		}
		return m, nil

	case deployTick:
		m.spin++
		// This timer animates only local pixels. Business state arrives from
		// committed stream events and never causes a network poll.
		if m.screen == screenDeploying && m.logContent == "" {
			return m, tickDeploy()
		}
		if (m.screen == screenMergePreviewing && m.logContent == "") ||
			(m.screen == screenMerging && !m.mergeTerminal) {
			return m, tickDeploy()
		}
		return m, nil

	case liveGateResult:
		if bool(msg) {
			m.liveGate = "passed"
		} else {
			m.liveGate = "failed"
		}
		return m, nil

	case deployDone:
		m.logContent = string(msg)
		m.liveGate = "checking"
		return m, checkLiveGate

	case deployError:
		return updateDeployFailure(m, string(msg))
	}

	return m, nil
}

func updateDeployFailure(m model, raw string) (tea.Model, tea.Cmd) {
	cause := extractReleaseError(raw)
	var sb strings.Builder
	sb.WriteString(red.Render("Deploy failed") + "\n\n")
	if hint := explainDeployError(cause); hint != "" {
		sb.WriteString(yellow.Render(hint) + "\n\n")
	}
	sb.WriteString(cause)
	m.logContent = sb.String()
	return m, nil
}

// extractReleaseError digs the innermost human-meaningful error out of the
// runner's output. The runner prints {"status":"FAILED","error":"..."} and,
// when the failure happened on gandalf, the error field itself embeds another
// such JSON blob — peel every layer so the screen shows the real cause once,
// not a wall of escaped JSON.
func extractReleaseError(out string) string {
	msg := strings.TrimSpace(out)
	for {
		start := strings.LastIndex(msg, `{"status"`)
		if start < 0 {
			break
		}
		var j struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(msg[start:]), &j) != nil || j.Error == "" {
			break
		}
		msg = strings.TrimSpace(j.Error)
	}
	// inner ssh failures prefix the message with the failed command line —
	// drop it, the cause after the last newline is what matters
	if i := strings.LastIndex(msg, "\n"); i >= 0 {
		if tail := strings.TrimSpace(msg[i+1:]); tail != "" {
			msg = tail
		}
	}
	return msg
}

// explainDeployError maps known runner errors to a one-line plain
// explanation shown above the raw error.
func explainDeployError(msg string) string {
	switch {
	case strings.Contains(msg, "production is not idle"):
		return "生产上有采集正在跑,SOP 拒绝中途部署;等采集结束再按 d 即可"
	case strings.Contains(msg, "another production port 80 release is running"):
		return "已有另一个 :80 发布在进行中,等它结束"
	case strings.Contains(msg, "byte length mismatch"):
		return "manifest 里的 runner 和仓库版本不一致,需要重新 prepare"
	case strings.Contains(msg, "not healthy enough to use"):
		return "生产数据库容器状态异常,先检查 gandalf 上的 db 容器"
	}
	return ""
}

// checkContentGate refuses to deploy a web image whose Explore chunk lost
// the 歌曲使用量 column — the 2026-08-05 incident: an image built without
// --build-arg VITE_SONG_USAGE_ENABLED=true gets the column tree-shaken out,
// while images.txt's song_usage_enabled=true is a hardcoded claim that the
// runner never verifies against image content. This gate checks the artifact.
func checkContentGate(webImage string) tea.Cmd {
	return func() tea.Msg {
		if webImage == "" {
			return gateResult{ok: false, info: "manifest has no web_image"}
		}
		out := sshRun(fmt.Sprintf(
			`docker run --rm --entrypoint sh %s -c 'grep -l 歌曲使用量 /usr/share/nginx/html/assets/Explore-*.js 2>/dev/null | wc -l'`,
			webImage))
		n := 0
		fmt.Sscan(strings.TrimSpace(out), &n)
		return gateResult{ok: n >= 1, info: out}
	}
}

// checkLiveGate re-runs the same content check against the *running* :80
// proxy container right after apply, so a bad rollout is caught immediately.
func checkLiveGate() tea.Msg {
	out := sshRun(`docker exec datacenter-kimi-production-proxy-1 sh -c 'grep -l 歌曲使用量 /usr/share/nginx/html/assets/Explore-*.js 2>/dev/null | wc -l'`)
	n := 0
	fmt.Sscan(strings.TrimSpace(out), &n)
	return liveGateResult(n >= 1)
}

func tickDeploy() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return deployTick(t) })
}

func parseLedgerPhase(line string) (ledgerPhase, error) {
	var record struct {
		At     string `json:"at"`
		Phase  string `json:"phase"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return ledgerPhase{}, fmt.Errorf("invalid ledger JSON: %w", err)
	}
	if record.At == "" || record.Phase == "" || record.Status == "" {
		return ledgerPhase{}, fmt.Errorf("invalid ledger event fields")
	}
	return ledgerPhase{at: record.At, phase: record.Phase, status: record.Status}, nil
}

// startDeployStream starts the local production orchestrator and opens one
// long-lived SSH tail of its append-only remote ledger. The process wait is
// the terminal state signal; no fixed-interval SSH or database polling occurs.
func startDeployStream(manifestPath, releaseID string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan deployStreamItem, 128)
		go func() {
			defer close(ch)
			if !validRemoteName(releaseID) {
				ch <- deployStreamItem{done: true, err: fmt.Errorf("invalid release identity")}
				return
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ledgerPath := "/home/gandalf/releases/datacenter-kimi/" + releaseID + "/ledger.jsonl"
			tailCmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", sshHost,
				"tail -n +1 -F "+ledgerPath)
			stdout, err := tailCmd.StdoutPipe()
			if err != nil {
				ch <- deployStreamItem{done: true, err: fmt.Errorf("open deploy event stream: %w", err)}
				return
			}
			if err := tailCmd.Start(); err != nil {
				ch <- deployStreamItem{done: true, err: fmt.Errorf("start deploy event stream: %w", err)}
				return
			}

			scanDone := make(chan struct{})
			go func() {
				defer close(scanDone)
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					phase, parseErr := parseLedgerPhase(scanner.Text())
					if parseErr == nil {
						ch <- deployStreamItem{phase: &phase}
					}
				}
			}()

			runner := exec.Command("bash",
				localRepo+"/scripts/release/production-80.sh",
				"apply", manifestPath, "--execute", "--allow-port80-downtime")
			runner.Dir = localRepo
			out, runErr := runner.CombinedOutput()

			// Give the file stream one short coalescing window to deliver the last
			// append already committed before the runner exited, then stop it.
			timer := time.NewTimer(300 * time.Millisecond)
			<-timer.C
			cancel()
			_ = tailCmd.Wait()
			<-scanDone

			output := string(out)
			if runErr == nil && strings.TrimSpace(output) == "" {
				output = "deploy completed"
			}
			ch <- deployStreamItem{output: output, err: runErr, done: true}
		}()
		return deployStreamReady{ch: ch}
	}
}

func waitDeployStream(ch <-chan deployStreamItem) tea.Cmd {
	return func() tea.Msg {
		item, ok := <-ch
		if !ok {
			return deployStreamMsg(deployStreamItem{
				done: true,
				err:  fmt.Errorf("deploy event stream ended before the runner result"),
			})
		}
		return deployStreamMsg(item)
	}
}

func migrationScriptPath() (string, error) {
	executable, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "migrate-songs.sh")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	if _, err := os.Stat("migrate-songs.sh"); err == nil {
		return "migrate-songs.sh", nil
	}
	return "", fmt.Errorf("migrate-songs.sh is not next to dp80 or in the working directory")
}

func newJobID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", prefix, time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b)), nil
}

// runMergePreview uses one cancellable attached SSH command. It is read-only
// with respect to both business databases and returns one exact plan token.
// The request identity prevents a late result from reopening a cancelled UI.
func runMergePreview(ctx context.Context, requestID string) tea.Cmd {
	return func() tea.Msg {
		fail := func(message string) tea.Msg {
			return mergePreviewError{requestID: requestID, message: message}
		}
		scriptPath, err := migrationScriptPath()
		if err != nil {
			return fail(err.Error())
		}
		jobID, err := newJobID("preview")
		if err != nil {
			return fail(err.Error())
		}
		remoteDir := "/home/gandalf/.local/state/dp80/merge-jobs/" + jobID
		remoteScript := remoteDir + "/migrate-songs.sh"
		if out, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", sshHost,
			"mkdir -p -m 0700 /home/gandalf/.local/state/dp80/merge-jobs && mkdir -m 0700 "+remoteDir).CombinedOutput(); err != nil {
			return fail("prepare preview: " + string(out) + err.Error())
		}
		defer exec.Command("ssh", "-o", "BatchMode=yes", sshHost, "rm -r -- "+remoteDir).Run()
		if out, err := exec.CommandContext(ctx, "scp", "-q", scriptPath, sshHost+":"+remoteScript).CombinedOutput(); err != nil {
			return fail("copy preview script: " + string(out) + err.Error())
		}
		out, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", sshHost,
			"bash "+remoteScript+" preview").CombinedOutput()
		if err != nil {
			return fail(strings.TrimSpace(string(out)) + "\n" + err.Error())
		}
		preview, err := parseMergePreview(out)
		if err != nil {
			return fail(err.Error())
		}
		return mergePreviewLoaded{requestID: requestID, preview: preview}
	}
}

// discardMergePreview removes a confirmed-but-unused token through the same
// validated script interface. It never touches business tables.
func discardMergePreview(previewID string) tea.Cmd {
	return func() tea.Msg {
		if !validRemoteName(previewID) || !strings.HasPrefix(previewID, "preview-") {
			return mergePreviewDiscardError("invalid preview identity")
		}
		scriptPath, err := migrationScriptPath()
		if err != nil {
			return mergePreviewDiscardError(err.Error())
		}
		jobID, err := newJobID("discard")
		if err != nil {
			return mergePreviewDiscardError(err.Error())
		}
		remoteDir := "/home/gandalf/.local/state/dp80/merge-jobs/" + jobID
		remoteScript := remoteDir + "/migrate-songs.sh"
		prepare := "mkdir -p -m 0700 /home/gandalf/.local/state/dp80/merge-jobs && mkdir -m 0700 " + remoteDir
		if out, err := exec.Command("ssh", "-o", "BatchMode=yes", sshHost, prepare).CombinedOutput(); err != nil {
			return mergePreviewDiscardError("prepare discard: " + strings.TrimSpace(string(out)) + err.Error())
		}
		defer exec.Command("ssh", "-o", "BatchMode=yes", sshHost, "rm -r -- "+remoteDir).Run()
		if out, err := exec.Command("scp", "-q", scriptPath, sshHost+":"+remoteScript).CombinedOutput(); err != nil {
			return mergePreviewDiscardError("copy discard script: " + strings.TrimSpace(string(out)) + err.Error())
		}
		if out, err := exec.Command("ssh", "-o", "BatchMode=yes", sshHost,
			"bash "+remoteScript+" discard "+previewID).CombinedOutput(); err != nil {
			return mergePreviewDiscardError(strings.TrimSpace(string(out)) + "\n" + err.Error())
		}
		return mergePreviewDiscarded{}
	}
}

// launchMerge creates one isolated remote job. The worker is detached, but
// every path is job-specific and the approved preview token is passed intact.
func launchMerge(preview mergePreview) tea.Cmd {
	return func() tea.Msg {
		scriptPath, err := migrationScriptPath()
		if err != nil {
			return mergeError(err.Error())
		}
		jobID, err := newJobID("merge")
		if err != nil {
			return mergeError(err.Error())
		}
		if !validRemoteName(jobID) || !validRemoteName(preview.PreviewID) {
			return mergeError("refusing invalid merge job identity")
		}
		remoteDir := "/home/gandalf/.local/state/dp80/merge-jobs/" + jobID
		remoteScript := remoteDir + "/migrate-songs.sh"
		prepare := "mkdir -p -m 0700 /home/gandalf/.local/state/dp80/merge-jobs && mkdir -m 0700 " + remoteDir
		if out, err := exec.Command("ssh", "-o", "BatchMode=yes", sshHost, prepare).CombinedOutput(); err != nil {
			return mergeError("prepare merge job: " + string(out) + err.Error())
		}
		if out, err := exec.Command("scp", "-q", scriptPath, sshHost+":"+remoteScript).CombinedOutput(); err != nil {
			return mergeError("copy merge script: " + string(out) + err.Error())
		}
		remoteLaunch := "cd " + remoteDir +
			" && : > events.jsonl && chmod 0600 events.jsonl" +
			" && nohup sh -c 'bash ./migrate-songs.sh apply " + preview.PreviewID +
			" >events.jsonl 2>&1; rc=$?; printf \"%s\\n\" \"$rc\" >exit-code'" +
			" </dev/null >/dev/null 2>&1 & echo $!"
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", sshHost, remoteLaunch).CombinedOutput()
		pid := strings.TrimSpace(string(out))
		if err != nil || pid == "" {
			return mergeError("launch merge: " + string(out) + fmt.Sprint(err))
		}
		if _, err := strconv.Atoi(pid); err != nil {
			return mergeError("launch merge returned an invalid pid")
		}
		return mergeJobLaunched(mergeJob{ID: jobID, PID: pid, RemoteDir: remoteDir})
	}
}

// startMergeStream opens one long-lived SSH stream. tail follows the job's
// append-only event file until the detached worker exits; no periodic SSH or
// database polling is used. A local spinner tick remains purely visual.
func startMergeStream(job mergeJob) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan mergeStreamItem, 128)
		go func() {
			defer close(ch)
			remote := "tail --pid=" + job.PID + " -n +1 -F " + job.RemoteDir +
				"/events.jsonl 2>&1; rc=$(cat " + job.RemoteDir +
				"/exit-code 2>/dev/null || printf 255); printf '" + exitPrefix + "%s\\n' \"$rc\""
			cmd := exec.Command("ssh", "-o", "BatchMode=yes", sshHost, remote)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				ch <- mergeStreamItem{err: err}
				return
			}
			if err := cmd.Start(); err != nil {
				ch <- mergeStreamItem{err: err}
				return
			}
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				ch <- parseMergeStreamLine(scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				ch <- mergeStreamItem{err: err}
			}
			if err := cmd.Wait(); err != nil {
				ch <- mergeStreamItem{err: fmt.Errorf("merge event stream closed: %w", err)}
			}
		}()
		return mergeStreamReady{ch: ch}
	}
}

func parseMergeStreamLine(line string) mergeStreamItem {
	item := mergeStreamItem{line: line}
	switch {
	case strings.HasPrefix(line, eventPrefix):
		var event mergeEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, eventPrefix)), &event); err != nil {
			item.err = fmt.Errorf("invalid merge event: %w", err)
		} else if event.SchemaVersion != 1 || event.Phase == "" || event.Status == "" {
			item.err = fmt.Errorf("invalid merge event fields")
		} else {
			item.event = &event
		}
	case strings.HasPrefix(line, exitPrefix):
		rc, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, exitPrefix)))
		if err != nil || rc < 0 || rc > 255 {
			item.err = fmt.Errorf("invalid merge exit code")
		} else {
			item.exitCode = &rc
		}
	}
	return item
}

func waitMergeStream(ch <-chan mergeStreamItem) tea.Cmd {
	return func() tea.Msg {
		item, ok := <-ch
		if !ok {
			return mergeStreamMsg(mergeStreamItem{err: fmt.Errorf("merge event stream ended without an exit code")})
		}
		return mergeStreamMsg(item)
	}
}

// formatAuditTable turns pipe-delimited psql rows (id|time|count|status)
// into a column-aligned table: widths are computed per column so every
// row lines up, instead of drifting with each promotion_id's length.
func formatAuditTable(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{dim.Render("(no migration history)")}
	}

	type row struct{ id, t, n, status string }
	var rows []row
	widths := [4]int{2, 4, 5, 6} // header minimums: id,time,count,status

	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "|")
		for len(f) < 4 {
			f = append(f, "")
		}
		r := row{
			truncateLine(strings.TrimSpace(f[0]), 28),
			strings.TrimSpace(f[1]),
			formatExactString(f[2]),
			strings.TrimSpace(f[3]),
		}
		rows = append(rows, r)
		widths[0] = max(widths[0], len([]rune(r.id)))
		widths[1] = max(widths[1], len([]rune(r.t)))
		widths[2] = max(widths[2], len([]rune(r.n)))
		widths[3] = max(widths[3], len([]rune(r.status)))
	}

	out := []string{fmt.Sprintf("%-*s  %-*s  %*s  %-*s",
		widths[0], "id", widths[1], "time", widths[2], "count", widths[3], "status")}
	for _, r := range rows {
		statusColor := dim
		switch r.status {
		case "completed":
			statusColor = green
		case "failed":
			statusColor = red
		case "running":
			statusColor = yellow
		}
		out = append(out, fmt.Sprintf("%-*s  %-*s  %*s  %s",
			widths[0], r.id, widths[1], r.t, widths[2], r.n,
			statusColor.Render(fmt.Sprintf("%-*s", widths[3], r.status))))
	}
	if len(out) > 9 {
		out = out[:9]
		out = append(out, dim.Render("…"))
	}
	return out
}

// tailFramed renders the last n visual lines of raw script output. Long
// lines are wrapped (not clipped) to the frame's text width — error
// messages must stay fully readable; only excess height is trimmed.
func tailFramed(raw string, n, inner int) string {
	var wrapped []string
	for _, l := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		r := []rune(l)
		if len(r) == 0 {
			wrapped = append(wrapped, "")
			continue
		}
		for len(r) > 0 {
			w := len(r)
			if w > inner {
				w = inner
			}
			wrapped = append(wrapped, string(r[:w]))
			r = r[w:]
		}
	}
	if len(wrapped) > n {
		wrapped = append([]string{dim.Render("… (earlier output omitted)")}, wrapped[len(wrapped)-n:]...)
	}
	return strings.Join(wrapped, "\n")
}

func appendMergeLog(current, line string) string {
	if strings.TrimSpace(line) == "" {
		return current
	}
	lines := strings.Split(strings.TrimRight(current, "\n"), "\n")
	if current == "" {
		lines = nil
	}
	lines = append(lines, line)
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	return strings.Join(lines, "\n")
}

// renderDeployProgress is the live deploy screen: content gate status, a
// progress bar scaled by committed ledger events, the phase checklist tail,
// elapsed time and a local-only spinner.
func (m model) renderDeployProgress(inner int) string {
	var sb strings.Builder
	spinner := spinFrames[m.spin%len(spinFrames)]
	elapsed := time.Since(m.deployStart).Round(time.Second)

	sb.WriteString(titleStyle.Render("Deploying → :80") + "  " +
		dim.Render(m.manifest.releaseID) + "\n\n")

	// content gate line
	switch m.gateState {
	case "checking":
		sb.WriteString(yellow.Render(spinner+" content gate: checking Explore 歌曲使用量 in candidate image …") + "\n\n")
	case "passed":
		sb.WriteString(green.Render("✓ content gate passed (Explore 歌曲使用量 present in image)") + "\n\n")
	}

	// progress bar from ledger phases
	completed := 0
	for _, p := range m.phases {
		if p.status == "completed" {
			completed++
		}
	}
	barW := inner - 12
	if barW > 40 {
		barW = 40
	}
	if barW < 10 {
		barW = 10
	}
	filled := completed * barW / applyTotalPhases
	if filled > barW {
		filled = barW
	}
	bar := green.Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", barW-filled))
	sb.WriteString(fmt.Sprintf("%s %s/%s\n\n", bar, formatExact(int64(completed)), formatExact(applyTotalPhases)))

	// phase checklist (tail — most recent phases)
	if m.gateState == "passed" && len(m.phases) == 0 {
		sb.WriteString(dim.Render(spinner+" waiting for runner to reach gandalf (ledger not written yet) …") + "\n")
	}
	show := m.phases
	if len(show) > 8 {
		sb.WriteString(dim.Render("  … "+formatExact(int64(len(show)-8))+" earlier phases") + "\n")
		show = show[len(show)-8:]
	}
	for i, p := range show {
		ts := p.at
		if len(ts) >= 19 {
			ts = ts[11:19]
		}
		last := i == len(show)-1
		switch {
		case p.status == "completed":
			sb.WriteString(green.Render("  ✓ ") + dim.Render(ts+" ") + p.phase + "\n")
		case last:
			sb.WriteString(yellow.Render("  "+spinner+" ") + dim.Render(ts+" ") + yellow.Render(p.phase+" ("+p.status+")") + "\n")
		default:
			sb.WriteString(dim.Render("  ○ "+ts+" "+p.phase+" ("+p.status+")") + "\n")
		}
	}
	if m.gateState == "passed" {
		sb.WriteString("\n" + yellow.Render(spinner) + dim.Render(fmt.Sprintf(" apply running — elapsed %s", elapsed)) + "\n")
	}
	return sb.String()
}

// renderMergeProgress is the live merge screen: spinner + elapsed, live song
// counts on both sides, the latest audit row, and a scrolling tail of the
// remote migration log — replacing the old static "running migration…" line
// that made a minutes-long merge look frozen.
func (m model) renderMergeProgress(inner int) string {
	var sb strings.Builder
	spinner := spinFrames[m.spin%len(spinFrames)]
	elapsed := time.Since(m.mergeStart).Round(time.Second)

	sb.WriteString(titleStyle.Render("Merging :8082 → :80") + "\n\n")
	if m.mergeJob.ID == "" {
		if m.mergeTerminal {
			sb.WriteString(red.Render("✗ merge job was not started") + "\n\n")
			if m.mergeLog != "" {
				sb.WriteString(dim.Render(tailFramed(m.mergeLog, 10, inner)) + "\n\n")
			}
			sb.WriteString(dim.Render("[Enter] back"))
			return sb.String()
		}
		sb.WriteString(yellow.Render(spinner+" creating isolated remote job …") + "\n")
		return sb.String()
	}
	statusLine := fmt.Sprintf("%s  phase=%s  elapsed=%s", m.mergeStatus, m.mergePhase, elapsed)
	if m.mergeTerminal {
		if m.mergeExitCode != nil {
			statusLine += fmt.Sprintf("  exit=%d", *m.mergeExitCode)
		}
		if m.mergeStatus == "committed" {
			sb.WriteString(green.Render("✓ "+statusLine) + "\n")
		} else {
			sb.WriteString(red.Render("✗ "+statusLine) + "\n")
		}
	} else {
		sb.WriteString(yellow.Render(spinner+" ") + dim.Render(statusLine) + "\n")
	}
	sb.WriteString(dim.Render("job: "+m.mergeJob.ID+"  pid: "+m.mergeJob.PID) + "\n\n")
	sb.WriteString(fmt.Sprintf("  approved songs       %s\n", bold.Render(formatExact(m.mergePreview.SongsToInsert))))
	sb.WriteString(fmt.Sprintf("  approved variants    %s\n", formatExact(m.mergePreview.VariantsToBackfill)))
	sb.WriteString(fmt.Sprintf("  production songs     %s → %s\n",
		formatExact(m.mergePreview.ProductionSongsBefore), formatExact(m.mergePreview.ProductionSongsAfter)))
	if m.mergeLog != "" {
		sb.WriteString("\n" + dim.Render(strings.Repeat("─", inner)) + "\n")
		sb.WriteString(dim.Render(tailFramed(m.mergeLog, 10, inner)) + "\n")
	}
	if m.mergeTerminal {
		sb.WriteString("\n" + dim.Render("[Enter] back"))
	} else {
		sb.WriteString("\n" + dim.Render("作业独立运行；退出面板不会终止合并"))
	}
	return sb.String()
}

type exactPlanRow struct {
	label string
	value int64
}

func writeExactPlanRows(sb *strings.Builder, rows []exactPlanRow) {
	for _, row := range rows {
		sb.WriteString(fmt.Sprintf("%-28s %s\n", row.label, formatExact(row.value)))
	}
}

// renderMergeConfirmation keeps every confirmation page short enough for a
// typical 80x24 terminal. Exact counts and both full digests remain available;
// Tab/right and Shift-Tab/left move between the fixed pages.
func (m model) renderMergeConfirmation() string {
	p := m.mergePreview
	var sb strings.Builder
	switch m.mergePreviewPage {
	case 1:
		sb.WriteString(titleStyle.Render("Exact merge details · source and inserts") + "\n\n")
		sb.WriteString("preview " + p.PreviewID + "\n")
		sb.WriteString("source " + p.SourceDigest + "\n")
		sb.WriteString("plan   " + p.PlanDigest + "\n\n")
		writeExactPlanRows(&sb, []exactPlanRow{
			{"source song rows", p.SourceSongRows},
			{"unique song keys", p.UniqueSongKeys},
			{"duplicate source rows", p.DuplicateSourceRows},
			{"duplicate source keys", p.DuplicateSourceSongKeys},
			{"source artist rows", p.SourceArtistRows},
			{"unique artists", p.UniqueArtists},
			{"frameworks insert", p.FrameworksToInsert},
			{"labels insert", p.LabelsToInsert},
			{"artists insert", p.ArtistsToInsert},
			{"songs insert", p.SongsToInsert},
			{"variant backfills", p.VariantsToBackfill},
		})
		sb.WriteString("\n" + dim.Render("[Tab/→] blockers  [Shift+Tab/←] summary  [Esc] cancel"))
	case 2:
		sb.WriteString(titleStyle.Render("Exact merge details · blockers and production") + "\n\n")
		writeExactPlanRows(&sb, []exactPlanRow{
			{"variant conflicts", p.VariantConflicts},
			{"invalid release dates", p.InvalidReleaseDates},
			{"duplicate prod songs", p.DuplicateProductionSongs},
			{"duplicate prod artists", p.DuplicateProductionArtists},
			{"duplicate prod labels", p.DuplicateProductionLabels},
			{"duplicate prod frameworks", p.DuplicateProductionFrameworks},
			{"missing variant targets", p.MissingVariantTargets},
			{"self variant links", p.SelfVariantLinks},
			{"variant cycles", p.VariantCycles},
			{"sequence config blockers", p.SequenceCacheBlockers},
			{"preserved field diffs", p.PreservedSongDifferences},
			{"sequence bumps", p.SequenceBumps},
			{"blocking conflicts", p.Blockers},
		})
		sb.WriteString(fmt.Sprintf("%-28s %s → %s\n", "production songs",
			formatExact(p.ProductionSongsBefore), formatExact(p.ProductionSongsAfter)))
		sb.WriteString("\n" + dim.Render("[Tab/→] summary  [Shift+Tab/←] source  [Esc] cancel"))
	default:
		sb.WriteString(titleStyle.Render("Exact merge plan :8082 → :80") + "\n\n")
		sb.WriteString("preview " + p.PreviewID + "\n\n")
		writeExactPlanRows(&sb, []exactPlanRow{
			{"frameworks insert", p.FrameworksToInsert},
			{"labels insert", p.LabelsToInsert},
			{"artists insert", p.ArtistsToInsert},
			{"songs insert", p.SongsToInsert},
			{"variant backfills", p.VariantsToBackfill},
			{"preserved field diffs", p.PreservedSongDifferences},
			{"blocking conflicts", p.Blockers},
		})
		sb.WriteString(fmt.Sprintf("%-28s %s → %s\n", "production songs",
			formatExact(p.ProductionSongsBefore), formatExact(p.ProductionSongsAfter)))
		if p.Blockers > 0 {
			categories := []exactPlanRow{
				{"duplicate source keys", p.DuplicateSourceSongKeys},
				{"duplicate prod songs", p.DuplicateProductionSongs},
				{"duplicate prod artists", p.DuplicateProductionArtists},
				{"duplicate prod labels", p.DuplicateProductionLabels},
				{"duplicate prod frameworks", p.DuplicateProductionFrameworks},
				{"missing variant targets", p.MissingVariantTargets},
				{"self variant links", p.SelfVariantLinks},
				{"variant cycles", p.VariantCycles},
				{"variant conflicts", p.VariantConflicts},
				{"invalid release dates", p.InvalidReleaseDates},
				{"sequence config blockers", p.SequenceCacheBlockers},
			}
			shown := 0
			for _, category := range categories {
				if category.value == 0 || shown == 3 {
					continue
				}
				writeExactPlanRows(&sb, []exactPlanRow{category})
				shown++
			}
			sb.WriteString("\n" + red.Render("REFUSED: resolve blockers and generate a new preview.") + "\n")
		} else {
			sb.WriteString("\n" + yellow.Render("Apply this exact immutable plan in one production transaction?") + "\n")
			sb.WriteString(cmdKey.Render("[M]") + dim.Render("ERGE exact plan") + "\n")
		}
		sb.WriteString("\n" + dim.Render("[Tab/→] exact details  [Esc] cancel"))
	}
	return sb.String()
}

func (m model) View() string {
	outer, inner, _, _ := m.dims()
	switch m.screen {
	case screenBoard:
		if m.loading {
			return renderFramed(dim.Render("loading…"), outer)
		}
		return renderFramed(m.renderBoardContent(), outer)

	case screenMergePreviewing:
		if m.logContent != "" {
			return renderFramed(tailFramed(m.logContent, 28, inner)+"\n\n"+dim.Render("[Esc] back"), outer)
		}
		spinner := spinFrames[m.spin%len(spinFrames)]
		content := titleStyle.Render("Preview :8082 → :80 merge") + "\n\n" +
			yellow.Render(spinner+" exporting one immutable source snapshot …") + "\n" +
			dim.Render("Then dp80 builds an exact, read-only production plan.") + "\n\n" +
			dim.Render("No production business row is written during preview.")
		return renderFramed(content, outer)

	case screenConfirmMerge:
		return renderFramed(m.renderMergeConfirmation(), outer)

	case screenConfirmCollect:
		cur := m.board.collectState
		var action string
		if cur == "off" {
			action = green.Render("将恢复采集(删除覆盖策略,回到代码默认排期)。")
		} else {
			action = yellow.Render("将停止 :80 全部采集(全平台歌曲/评论/发现/艺人粉丝全关,") + "\n" +
				yellow.Render("并把 running 状态的采集 run 标为 aborted)。策略即时生效。")
		}
		content := titleStyle.Render(":80 采集开关") + "\n\n" +
			fmt.Sprintf("%-10s %s\n\n", "current", bold.Render(cur)) +
			action + "\n\n" +
			cmdKey.Render("[y]") + dim.Render("es") + "\n" +
			cmdKey.Render("[n]") + dim.Render("/Esc cancel")
		return renderFramed(content, outer)

	case screenConfirmKill:
		running := fetchCollectRunning()
		content := titleStyle.Render("Kill :80 running collect") + "\n\n" +
			fmt.Sprintf("%-10s %s\n\n", "running", bold.Render(running)) +
			yellow.Render("将把所有 running 状态的 collect_run 标为 aborted。") + "\n" +
			yellow.Render("策略不变(on/off 保持原状)。") + "\n\n" +
			cmdKey.Render("[y]") + dim.Render("es") + "\n" +
			cmdKey.Render("[n]") + dim.Render("/Esc cancel")
		return renderFramed(content, outer)

	case screenConfirmDeploy:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Deploy release → :80") + "\n\n")
		if m.manifestErr != "" {
			sb.WriteString(red.Render("✗ "+truncateLine(m.manifestErr, inner-2)) + "\n\n")
			sb.WriteString(dim.Render("[Esc/n] back"))
			return renderFramed(sb.String(), outer)
		}
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "release", bold.Render(m.manifest.releaseID)))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "commit", cyan.Render(truncateLine(m.manifest.commit, 12))))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "backend", m.manifest.beVersion))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "web", m.manifest.weVersion))
		sb.WriteString(dim.Render("manifest: "+m.manifest.source) + "\n")
		sb.WriteString("\n" + yellow.Render("Runs production-80.sh apply (brief :80 downtime).") + "\n\n")
		sb.WriteString(cmdKey.Render("[y]") + dim.Render("es") + "\n")
		sb.WriteString(cmdKey.Render("[n]") + dim.Render("/Esc cancel"))
		return renderFramed(sb.String(), outer)

	case screenDeploying:
		if m.logContent == "" {
			return renderFramed(m.renderDeployProgress(inner), outer)
		}
		var extra string
		switch m.liveGate {
		case "checking":
			extra = yellow.Render("live gate: checking :80 Explore 歌曲使用量 …") + "\n"
		case "passed":
			extra = green.Render("live gate ✓ :80 Explore 歌曲使用量 present") + "\n"
		case "failed":
			extra = red.Render("live gate ✗ :80 Explore chunk 缺「歌曲使用量」— 别收工,先排查") + "\n"
		}
		return renderFramed(tailFramed(m.logContent, 26, inner)+"\n\n"+extra+dim.Render("[Enter] back"), outer)

	case screenMerging:
		return renderFramed(m.renderMergeProgress(inner), outer)

	case screenLog:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Jose · :80 Deploy Panel — Logs") + "\n\n")

		sb.WriteString(green.Bold(true).Render("MIGRATION HISTORY") + "\n")
		for _, line := range formatAuditTable(m.auditText) {
			sb.WriteString("  " + line + "\n")
		}
		sb.WriteString("\n")

		sb.WriteString(cyan.Bold(true).Render("BACKEND LOGS") + "\n")
		logLines := strings.Split(strings.TrimSpace(m.backendLogs), "\n")
		if len(logLines) == 0 || logLines[0] == "" {
			logLines = []string{"(no recent business activity)"}
		} else if len(logLines) > 12 {
			logLines = append(logLines[:12], "…")
		}
		for _, line := range logLines {
			sb.WriteString("  " + dim.Render(truncateLine(line, inner-2)) + "\n")
		}

		sb.WriteString("\n" + dim.Render(strings.Repeat("─", inner)) + "\n")
		sb.WriteString(dim.Render("[Esc/q] back"))
		return renderFramed(sb.String(), outer)
	}
	return ""
}

// ── entry point ───────────────────────────────────────────────────────────────

// detectWidth returns the terminal width for the non-interactive `status`
// command. Falls back to 0 (→ default 80-wide frame) when stdout is not a
// terminal (e.g. piped/captured).
func detectWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(appVersion)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "status" {
		d := fetchBoard()
		m := model{board: d, termWidth: detectWidth()}
		outer, _, _, _ := m.dims()
		fmt.Println(renderFramed(m.renderBoardContent(), outer))
		return
	}

	p := tea.NewProgram(model{loading: true}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
