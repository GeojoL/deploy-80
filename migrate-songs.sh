#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# dp80 :8082 -> :80 business-data merge.
#
# The command has two deliberately separate phases:
#   preview              export one immutable source snapshot and build an exact plan
#   apply <preview_id>   lock production, rebuild that plan, compare it row-for-row,
#                        then apply all business-table changes in one transaction
#
# No password, dblink, Docker-network mutation, or shared temporary filename is used.

readonly DEFAULT_SRC="datacenter-kimi-db-1"
readonly DEFAULT_DST="datacenter-kimi-production-db-1"
readonly DEFAULT_SRC_DB="datacenter_kimi_test"
readonly DEFAULT_DST_DB="datacenter_kimi_production"
readonly DEFAULT_PREVIEW_ROOT="/tmp/dp80-merge-previews"
readonly PG_USER="datacenter"

SRC="$DEFAULT_SRC"
DST="$DEFAULT_DST"
SRC_DB="$DEFAULT_SRC_DB"
DST_DB="$DEFAULT_DST_DB"
PREVIEW_ROOT="$DEFAULT_PREVIEW_ROOT"

MODE="${1:-}"
TOKEN="${2:-}"
WORK_DIR=""
CONTAINER_PREFIX=""
PROMOTION_ID=""
FINALIZED=no
PREVIEW_PERSISTED=no
SOURCE_CHECK_FILE=""
PREVIEW_LOCK_DIR=""
PREVIEW_LOCK_OWNED=no

emit_event() {
    local phase="$1" status="$2" message="$3"
    printf 'DP80_EVENT_JSON={"schema_version":1,"phase":"%s","status":"%s","message":"%s"}\n' \
        "$phase" "$status" "$message"
}

die() {
    printf '[ERROR] %s\n' "$*" >&2
    exit 1
}

is_uint() {
    [[ "$1" =~ ^[0-9]+$ ]]
}

is_digest() {
    [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

cleanup() {
    local rc=$?
    trap - EXIT

    if [[ -n "$CONTAINER_PREFIX" ]]; then
        docker exec "$DST" rm -f \
            "${CONTAINER_PREFIX}.snapshot" \
            "${CONTAINER_PREFIX}.common.sql" \
            "${CONTAINER_PREFIX}.plan" \
            "${CONTAINER_PREFIX}.expected-plan" \
            >/dev/null 2>&1 || true
    fi

    if [[ -n "$SOURCE_CHECK_FILE" ]]; then
        rm -f -- "$SOURCE_CHECK_FILE" >/dev/null 2>&1 || true
    fi

    if [[ "$MODE" == apply && "$FINALIZED" != yes && -n "$PROMOTION_ID" ]]; then
        docker exec -i "$DST" psql -X --no-password -q \
            -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 \
            -v promotion_id="$PROMOTION_ID" -v exit_code="$rc" <<'SQL' >/dev/null 2>&1 || true
UPDATE data_promotion_runs
SET status = 'failed',
    completed_at = now(),
    result_json = coalesce(result_json, '{}'::jsonb) ||
        jsonb_build_object('failure_phase', 'apply', 'exit_code', :'exit_code'::integer)
WHERE promotion_id = :'promotion_id' AND status = 'running';
SQL
    fi

    if [[ "$MODE" == apply && "$FINALIZED" != yes && "$PREVIEW_LOCK_OWNED" == yes \
        && -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
        rm -rf -- "$WORK_DIR"
        PREVIEW_LOCK_DIR=""
    elif [[ "$PREVIEW_LOCK_OWNED" == yes && -n "$PREVIEW_LOCK_DIR" && -d "$PREVIEW_LOCK_DIR" ]]; then
        rmdir -- "$PREVIEW_LOCK_DIR" >/dev/null 2>&1 || true
    fi

    if [[ "$MODE" == preview && "$PREVIEW_PERSISTED" != yes && -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
        rm -rf -- "$WORK_DIR"
    fi

    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

configure_runtime() {
    if [[ "${DP80_TEST_FIXTURE:-}" == 1 ]]; then
        [[ "${DP80_TEST_RUN_ID:-}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ ]] \
            || die "DP80_TEST_RUN_ID is invalid"
        [[ -n "${DP80_TEST_SRC_CONTAINER:-}" && -n "${DP80_TEST_DST_CONTAINER:-}" \
            && -n "${DP80_TEST_SRC_DB:-}" && -n "${DP80_TEST_DST_DB:-}" \
            && -n "${DP80_TEST_PREVIEW_ROOT:-}" ]] \
            || die "isolated fixture overrides are incomplete"
        [[ "$DP80_TEST_SRC_CONTAINER" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ \
            && "$DP80_TEST_DST_CONTAINER" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ \
            && "$DP80_TEST_SRC_DB" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ \
            && "$DP80_TEST_DST_DB" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] \
            || die "isolated fixture names are invalid"
        [[ "$DP80_TEST_PREVIEW_ROOT" == /tmp/dp80-test-* ]] \
            || die "isolated fixture preview root must be under /tmp/dp80-test-*"
        SRC="$DP80_TEST_SRC_CONTAINER"
        DST="$DP80_TEST_DST_CONTAINER"
        SRC_DB="$DP80_TEST_SRC_DB"
        DST_DB="$DP80_TEST_DST_DB"
        PREVIEW_ROOT="$DP80_TEST_PREVIEW_ROOT"
        return
    fi

    local name
    for name in DP80_TEST_RUN_ID DP80_TEST_SRC_CONTAINER DP80_TEST_DST_CONTAINER \
        DP80_TEST_SRC_DB DP80_TEST_DST_DB DP80_TEST_PREVIEW_ROOT; do
        [[ -z "${!name:-}" ]] || die "$name is only allowed with DP80_TEST_FIXTURE=1"
    done
}

require_runtime() {
    command -v docker >/dev/null 2>&1 || die "docker is unavailable"
    command -v sha256sum >/dev/null 2>&1 || die "sha256sum is unavailable"
    docker inspect "$SRC" >/dev/null 2>&1 || die "source container is unavailable: $SRC"
    docker inspect "$DST" >/dev/null 2>&1 || die "destination container is unavailable: $DST"
    if [[ "${DP80_TEST_FIXTURE:-}" == 1 ]]; then
        local source_label destination_label
        source_label=$(docker inspect --format '{{ index .Config.Labels "dp80.test-fixture" }}' "$SRC")
        destination_label=$(docker inspect --format '{{ index .Config.Labels "dp80.test-fixture" }}' "$DST")
        [[ "$source_label" == "$DP80_TEST_RUN_ID" && "$destination_label" == "$DP80_TEST_RUN_ID" ]] \
            || die "isolated fixture container labels do not match DP80_TEST_RUN_ID"
    fi
}

export_snapshot() {
    local snapshot="$1" announce="${2:-yes}"
    if [[ "$announce" == yes ]]; then
        emit_event snapshot running "exporting one source statement snapshot"
    fi
    docker exec -i "$SRC" psql -X --no-password -qAt \
        -U "$PG_USER" -d "$SRC_DB" -v ON_ERROR_STOP=1 >"$snapshot" <<'SQL'
COPY (
  WITH records AS (
    SELECT 1 AS record_order, s.id::bigint AS record_id,
           jsonb_build_object(
             'record_type', 'song',
             'source_id', s.id,
             'name', s.name,
             'artist_name', a.name,
             'label_name', l.name,
             'framework_name', f.name,
             'lyricist', s.lyricist,
             'composer', s.composer,
             'upload_date', s.upload_date,
             'release_date', s.release_date,
             'lyrics_url', s.lyrics_url,
             'variant_source_id', s.variant_of_id,
             'variant_found', vs.id IS NOT NULL,
             'variant_name', vs.name,
             'variant_artist_name', va.name
           ) AS payload
    FROM songs s
    LEFT JOIN artists a ON a.id = s.artist_id
    LEFT JOIN labels l ON l.id = s.label_id
    LEFT JOIN frameworks f ON f.id = s.framework_id
    LEFT JOIN songs vs ON vs.id = s.variant_of_id
    LEFT JOIN artists va ON va.id = vs.artist_id

    UNION ALL

    SELECT 2, a.id::bigint,
           jsonb_build_object(
             'record_type', 'artist',
             'source_id', a.id,
             'name', a.name
           )
    FROM artists a
  )
  SELECT payload::text
  FROM records
  ORDER BY record_order, record_id
) TO STDOUT;
SQL
    [[ -s "$snapshot" ]] || die "source snapshot is empty"
    if [[ "$announce" == yes ]]; then
        emit_event snapshot completed "source snapshot exported"
    fi
}

write_common_sql() {
    local path="$1"
    cat >"$path" <<'SQL'
CREATE TEMP TABLE stage_raw (line text) ON COMMIT DROP;
COPY stage_raw(line) FROM :'snapshot_path';

CREATE TEMP TABLE stage_records ON COMMIT DROP AS
SELECT line::jsonb AS payload
FROM stage_raw;

CREATE TEMP TABLE stage_songs_raw ON COMMIT DROP AS
SELECT (payload->>'source_id')::bigint AS source_id,
       payload->>'name' AS name,
       payload->>'artist_name' AS artist_name,
	   jsonb_build_array(payload->>'name', payload->>'artist_name')::text AS song_key,
       payload->>'label_name' AS label_name,
       payload->>'framework_name' AS framework_name,
       payload->>'lyricist' AS lyricist,
       payload->>'composer' AS composer,
       nullif(payload->>'upload_date', '')::date AS upload_date,
       nullif(payload->>'release_date', '')::date AS release_date,
       payload->>'lyrics_url' AS lyrics_url,
       nullif(payload->>'variant_source_id', '')::bigint AS variant_source_id,
       coalesce((payload->>'variant_found')::boolean, false) AS variant_found,
       payload->>'variant_name' AS variant_name,
       payload->>'variant_artist_name' AS variant_artist_name
FROM stage_records
WHERE payload->>'record_type' = 'song';

CREATE TEMP TABLE stage_artists_raw ON COMMIT DROP AS
SELECT (payload->>'source_id')::bigint AS source_id,
       payload->>'name' AS name
FROM stage_records
WHERE payload->>'record_type' = 'artist';

CREATE TEMP TABLE source_song_keys ON COMMIT DROP AS
SELECT song_key, name, artist_name, count(*)::bigint AS row_count
FROM stage_songs_raw
GROUP BY song_key, name, artist_name;

CREATE TEMP TABLE stage_songs ON COMMIT DROP AS
SELECT s.*
FROM stage_songs_raw s
JOIN source_song_keys k
  ON k.song_key = s.song_key
WHERE k.row_count = 1;

CREATE INDEX stage_songs_source_id_idx ON stage_songs(source_id);
CREATE INDEX stage_songs_song_key_idx ON stage_songs(song_key);
CREATE INDEX source_song_keys_song_key_idx ON source_song_keys(song_key);

CREATE TEMP TABLE stage_artists ON COMMIT DROP AS
SELECT name, min(source_id)::bigint AS source_id
FROM stage_artists_raw
GROUP BY name;

CREATE TEMP TABLE target_song_keys ON COMMIT DROP AS
SELECT jsonb_build_array(s.name, a.name)::text AS song_key,
       s.name, a.name AS artist_name, count(*)::bigint AS row_count,
       min(s.id)::bigint AS target_id
FROM songs s
LEFT JOIN artists a ON a.id = s.artist_id
GROUP BY s.name, a.name;

CREATE INDEX target_song_keys_song_key_idx ON target_song_keys(song_key);

-- Fresh TEMP tables have no planner statistics. Without these ANALYZE calls,
-- PostgreSQL can choose multi-minute nested loops for a zero-row variant check.
ANALYZE stage_songs_raw;
ANALYZE source_song_keys;
ANALYZE stage_songs;
ANALYZE stage_artists;
ANALYZE target_song_keys;

CREATE TEMP TABLE target_artist_keys ON COMMIT DROP AS
SELECT name, count(*)::bigint AS row_count, min(id)::bigint AS target_id
FROM artists GROUP BY name;

CREATE TEMP TABLE target_label_keys ON COMMIT DROP AS
SELECT name, count(*)::bigint AS row_count, min(id)::bigint AS target_id
FROM labels GROUP BY name;

CREATE TEMP TABLE target_framework_keys ON COMMIT DROP AS
SELECT name, count(*)::bigint AS row_count, min(id)::bigint AS target_id
FROM frameworks GROUP BY name;

CREATE TEMP TABLE merge_plan (
  action text NOT NULL,
  business_key jsonb NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  blocking_reason text
) ON COMMIT DROP;

-- Ambiguous input/target keys are blockers; the merge never chooses min(id).
INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'source_song_key', 'name', name, 'artist_name', artist_name),
       jsonb_build_object('rows', row_count),
       'duplicate source song content key'
FROM source_song_keys WHERE row_count > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'target_song_key', 'name', t.name, 'artist_name', t.artist_name),
       jsonb_build_object('rows', t.row_count),
       'duplicate production song content key'
FROM target_song_keys t
JOIN source_song_keys s
  ON s.song_key = t.song_key
WHERE t.row_count > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker', jsonb_build_object('kind', 'artist_name', 'name', t.name),
       jsonb_build_object('rows', t.row_count), 'duplicate production artist name'
FROM target_artist_keys t
JOIN stage_artists s ON s.name IS NOT DISTINCT FROM t.name
WHERE t.row_count > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker', jsonb_build_object('kind', 'label_name', 'name', t.name),
       jsonb_build_object('rows', t.row_count), 'duplicate production label name'
FROM target_label_keys t
JOIN (SELECT DISTINCT label_name AS name FROM stage_songs WHERE label_name IS NOT NULL) s
  ON s.name IS NOT DISTINCT FROM t.name
WHERE t.row_count > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker', jsonb_build_object('kind', 'framework_name', 'name', t.name),
       jsonb_build_object('rows', t.row_count), 'duplicate production framework name'
FROM target_framework_keys t
JOIN (SELECT DISTINCT framework_name AS name FROM stage_songs WHERE framework_name IS NOT NULL) s
  ON s.name IS NOT DISTINCT FROM t.name
WHERE t.row_count > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'variant', 'source_id', source_id),
       jsonb_build_object('variant_source_id', variant_source_id),
       'variant target is missing from the source snapshot'
FROM stage_songs
WHERE variant_source_id IS NOT NULL AND NOT variant_found;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'variant', 'name', name, 'artist_name', artist_name),
       jsonb_build_object('variant_name', variant_name, 'variant_artist_name', variant_artist_name),
       'variant points to the same content key'
FROM stage_songs
WHERE variant_source_id IS NOT NULL
  AND name IS NOT DISTINCT FROM variant_name
  AND artist_name IS NOT DISTINCT FROM variant_artist_name;

-- Reject every source chain that reaches a multi-row cycle. A self-link has
-- its own category above; longer cycles are never written to production.
WITH RECURSIVE variant_walk AS (
  SELECT source_id AS start_id, source_id AS current_id,
         variant_source_id AS next_id, ARRAY[source_id]::bigint[] AS path
  FROM stage_songs
  WHERE variant_source_id IS NOT NULL

  UNION ALL

  SELECT w.start_id, parent.source_id, parent.variant_source_id,
         w.path || parent.source_id
  FROM variant_walk w
  JOIN stage_songs parent ON parent.source_id = w.next_id
  WHERE w.next_id IS NOT NULL
    AND NOT w.next_id = ANY(w.path)
), cycle_starts AS (
  SELECT DISTINCT start_id
  FROM variant_walk
  WHERE next_id IS NOT NULL
    AND next_id = ANY(path)
    AND next_id <> current_id
)
INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'variant_cycle', 'source_id', s.source_id,
                          'name', s.name, 'artist_name', s.artist_name),
       '{}'::jsonb,
       'variant chain reaches a cycle'
FROM cycle_starts c
JOIN stage_songs s ON s.source_id = c.start_id;

-- Dictionary and song inserts are exact plan rows, not inferred from count deltas.
INSERT INTO merge_plan(action, business_key, payload)
SELECT 'insert_framework', jsonb_build_object('name', s.name), jsonb_build_object('name', s.name)
FROM (SELECT DISTINCT framework_name AS name FROM stage_songs WHERE framework_name IS NOT NULL) s
LEFT JOIN target_framework_keys t ON t.name IS NOT DISTINCT FROM s.name
WHERE t.name IS NULL;

INSERT INTO merge_plan(action, business_key, payload)
SELECT 'insert_label', jsonb_build_object('name', s.name), jsonb_build_object('name', s.name)
FROM (SELECT DISTINCT label_name AS name FROM stage_songs WHERE label_name IS NOT NULL) s
LEFT JOIN target_label_keys t ON t.name IS NOT DISTINCT FROM s.name
WHERE t.name IS NULL;

INSERT INTO merge_plan(action, business_key, payload)
SELECT 'insert_artist', jsonb_build_object('name', s.name), jsonb_build_object('name', s.name)
FROM stage_artists s
LEFT JOIN target_artist_keys t ON t.name IS NOT DISTINCT FROM s.name
WHERE s.name IS NOT NULL AND t.name IS NULL;

INSERT INTO merge_plan(action, business_key, payload)
SELECT 'insert_song',
       jsonb_build_object('name', s.name, 'artist_name', s.artist_name),
       jsonb_build_object(
         'name', s.name, 'artist_name', s.artist_name,
         'label_name', s.label_name, 'framework_name', s.framework_name,
         'lyricist', s.lyricist, 'composer', s.composer,
         'upload_date', s.upload_date, 'release_date', s.release_date,
         'lyrics_url', s.lyrics_url
       )
FROM stage_songs s
LEFT JOIN target_song_keys t
  ON t.song_key = s.song_key
WHERE t.target_id IS NULL;

-- Existing song-field differences are preserved, but made visible in the plan.
INSERT INTO merge_plan(action, business_key, payload)
SELECT 'preserve_song_difference',
       jsonb_build_object('name', s.name, 'artist_name', s.artist_name),
       jsonb_build_object(
         'source', jsonb_build_object(
           'label_name', s.label_name, 'framework_name', s.framework_name,
           'lyricist', s.lyricist, 'composer', s.composer,
           'upload_date', s.upload_date, 'release_date', s.release_date,
           'lyrics_url', s.lyrics_url),
         'production', jsonb_build_object(
           'label_name', tl.name, 'framework_name', tf.name,
           'lyricist', t.lyricist, 'composer', t.composer,
           'upload_date', t.upload_date, 'release_date', t.release_date,
           'lyrics_url', t.lyrics_url)
       )
FROM stage_songs s
JOIN target_song_keys tk
  ON tk.song_key = s.song_key
 AND tk.row_count = 1
JOIN songs t ON t.id = tk.target_id
LEFT JOIN labels tl ON tl.id = t.label_id
LEFT JOIN frameworks tf ON tf.id = t.framework_id
WHERE (s.label_name, s.framework_name, s.lyricist, s.composer,
       s.upload_date, s.release_date, s.lyrics_url)
  IS DISTINCT FROM
      (tl.name, tf.name, t.lyricist, t.composer,
       t.upload_date, t.release_date, t.lyrics_url);

-- A variant backfill is planned for both existing and newly inserted children.
INSERT INTO merge_plan(action, business_key, payload)
SELECT 'update_variant',
       jsonb_build_object('name', child.name, 'artist_name', child.artist_name),
       jsonb_build_object('variant_name', parent.name, 'variant_artist_name', parent.artist_name)
FROM stage_songs child
JOIN stage_songs parent ON parent.source_id = child.variant_source_id
LEFT JOIN target_song_keys child_key
  ON child_key.song_key = child.song_key
LEFT JOIN songs target_child ON target_child.id = child_key.target_id AND child_key.row_count = 1
WHERE child.variant_source_id IS NOT NULL
  AND child.song_key <> parent.song_key
  AND (child_key.target_id IS NULL OR target_child.variant_of_id IS NULL);

-- Existing, different variant links are never overwritten silently.
INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker',
       jsonb_build_object('kind', 'variant_conflict', 'name', child.name, 'artist_name', child.artist_name),
       jsonb_build_object(
         'source_variant_name', parent.name,
         'source_variant_artist_name', parent.artist_name,
         'production_variant_name', current_parent.name,
         'production_variant_artist_name', current_parent_artist.name),
       'production variant_of_id differs from the source plan'
FROM stage_songs child
JOIN stage_songs parent ON parent.source_id = child.variant_source_id
JOIN target_song_keys child_key
  ON child_key.song_key = child.song_key
 AND child_key.row_count = 1
JOIN songs target_child ON target_child.id = child_key.target_id
JOIN songs current_parent ON current_parent.id = target_child.variant_of_id
LEFT JOIN artists current_parent_artist ON current_parent_artist.id = current_parent.artist_id
WHERE target_child.variant_of_id IS NOT NULL
  AND jsonb_build_array(current_parent.name, current_parent_artist.name)::text <> parent.song_key;

-- Sequence repair is explicit: move a lagging value forward, or mark an
-- uncalled value at max(id) as called so the next value cannot collide.
CREATE TEMP TABLE sequence_state ON COMMIT DROP AS
WITH table_state(table_name, max_id, sequence_name) AS (
  SELECT 'frameworks', coalesce(max(id), 0)::bigint, pg_get_serial_sequence('frameworks', 'id') FROM frameworks
  UNION ALL
  SELECT 'labels', coalesce(max(id), 0)::bigint, pg_get_serial_sequence('labels', 'id') FROM labels
  UNION ALL
  SELECT 'artists', coalesce(max(id), 0)::bigint, pg_get_serial_sequence('artists', 'id') FROM artists
  UNION ALL
  SELECT 'songs', coalesce(max(id), 0)::bigint, pg_get_serial_sequence('songs', 'id') FROM songs
)
SELECT t.table_name, t.max_id, t.sequence_name,
	       q.last_value::bigint AS last_value, false AS is_called,
	       q.cache_size::bigint AS cache_size
FROM table_state t
LEFT JOIN pg_sequences q
  ON q.schemaname = split_part(t.sequence_name, '.', 1)
 AND q.sequencename = split_part(t.sequence_name, '.', 2);

-- pg_sequences does not expose is_called. Read that flag from each resolved
-- sequence relation so last_value=max(id), is_called=true is correctly treated
-- as healthy rather than reported as a fake bump.
DO $sequence_flags$
DECLARE p record; called boolean;
BEGIN
  FOR p IN SELECT table_name, sequence_name FROM sequence_state WHERE sequence_name IS NOT NULL LOOP
    EXECUTE format('SELECT is_called FROM %s', p.sequence_name::regclass) INTO called;
    UPDATE sequence_state SET is_called = called WHERE table_name = p.table_name;
  END LOOP;
END
$sequence_flags$;

INSERT INTO merge_plan(action, business_key, payload)
SELECT 'sequence_bump', jsonb_build_object('table', table_name),
       jsonb_build_object('sequence_name', sequence_name, 'old_value', last_value,
	                          'old_is_called', is_called, 'new_value', max_id,
	                          'cache_size', cache_size)
FROM sequence_state
WHERE sequence_name IS NOT NULL
  AND max_id > 0
  AND (coalesce(last_value, 0) < max_id OR (last_value = max_id AND NOT is_called));

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker', jsonb_build_object('kind', 'sequence_cache', 'table', table_name),
       jsonb_build_object('sequence_name', sequence_name, 'cache_size', cache_size),
       'sequence repair requires cache_size=1'
FROM sequence_state
WHERE sequence_name IS NOT NULL
  AND max_id > 0
  AND (coalesce(last_value, 0) < max_id OR (last_value = max_id AND NOT is_called))
  AND coalesce(cache_size, 1) > 1;

INSERT INTO merge_plan(action, business_key, payload, blocking_reason)
SELECT 'blocker', jsonb_build_object('kind', 'sequence_config', 'table', table_name),
       '{}'::jsonb, 'required sequence is missing'
FROM sequence_state
WHERE sequence_name IS NULL;
SQL
}

copy_into_destination() {
    local host_path="$1" container_path="$2"
    docker cp "$host_path" "$DST:$container_path" >/dev/null
    docker exec "$DST" chmod 0644 "$container_path"
}

parse_metrics() {
    local line="$1"
    IFS='|' read -r marker \
        SOURCE_SONG_ROWS UNIQUE_SONG_KEYS DUPLICATE_SOURCE_ROWS DUPLICATE_SOURCE_KEYS \
        SOURCE_ARTIST_ROWS UNIQUE_ARTISTS FRAMEWORKS_INSERT LABELS_INSERT \
        ARTISTS_INSERT SONGS_INSERT VARIANT_BACKFILLS VARIANT_CONFLICTS \
        INVALID_RELEASE_DATES DUPLICATE_PRODUCTION_SONG_KEYS \
        DUPLICATE_PRODUCTION_ARTIST_NAMES DUPLICATE_PRODUCTION_LABEL_NAMES \
        DUPLICATE_PRODUCTION_FRAMEWORK_NAMES MISSING_VARIANT_TARGETS \
        SELF_VARIANT_LINKS SEQUENCE_CACHE_BLOCKERS PRESERVED_DIFFERENCES BLOCKERS \
        VARIANT_CYCLES PRODUCTION_SONGS_BEFORE PRODUCTION_SONGS_AFTER SEQUENCE_BUMPS <<<"$line"
    [[ "$marker" == DP80_METRICS_V1 ]] || die "preview returned an invalid metrics protocol"
    local value
    for value in "$SOURCE_SONG_ROWS" "$UNIQUE_SONG_KEYS" "$DUPLICATE_SOURCE_ROWS" \
        "$DUPLICATE_SOURCE_KEYS" \
        "$SOURCE_ARTIST_ROWS" "$UNIQUE_ARTISTS" "$FRAMEWORKS_INSERT" \
        "$LABELS_INSERT" "$ARTISTS_INSERT" "$SONGS_INSERT" \
        "$VARIANT_BACKFILLS" "$VARIANT_CONFLICTS" "$INVALID_RELEASE_DATES" \
        "$DUPLICATE_PRODUCTION_SONG_KEYS" "$DUPLICATE_PRODUCTION_ARTIST_NAMES" \
        "$DUPLICATE_PRODUCTION_LABEL_NAMES" "$DUPLICATE_PRODUCTION_FRAMEWORK_NAMES" \
        "$MISSING_VARIANT_TARGETS" "$SELF_VARIANT_LINKS" "$SEQUENCE_CACHE_BLOCKERS" \
        "$PRESERVED_DIFFERENCES" "$BLOCKERS" "$VARIANT_CYCLES" "$PRODUCTION_SONGS_BEFORE" \
        "$PRODUCTION_SONGS_AFTER" "$SEQUENCE_BUMPS"; do
        is_uint "$value" || die "preview returned a non-numeric metric"
    done
}

run_preview() {
    mkdir -p "$PREVIEW_ROOT"
    chmod 0700 "$PREVIEW_ROOT"
    WORK_DIR=$(mktemp -d "$PREVIEW_ROOT/preview-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXXXX")
    TOKEN=${WORK_DIR##*/}
    CONTAINER_PREFIX="/tmp/dp80-${TOKEN}"

    export_snapshot "$WORK_DIR/snapshot.jsonl"
    local snapshot_digest
    snapshot_digest=$(sha256sum "$WORK_DIR/snapshot.jsonl" | awk '{print $1}')
    is_digest "$snapshot_digest" || die "cannot calculate source snapshot digest"

    write_common_sql "$WORK_DIR/common.sql"
    copy_into_destination "$WORK_DIR/snapshot.jsonl" "${CONTAINER_PREFIX}.snapshot"
    copy_into_destination "$WORK_DIR/common.sql" "${CONTAINER_PREFIX}.common.sql"

    emit_event plan running "building exact production plan"
    local metrics
    metrics=$(docker exec -i "$DST" psql -X --no-password -qAt \
        -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 \
        -v snapshot_path="${CONTAINER_PREFIX}.snapshot" \
        -v plan_path="${CONTAINER_PREFIX}.plan" <<SQL
-- PostgreSQL 17 rejects CREATE TEMP TABLE in a READ ONLY transaction. This
-- repeatable-read transaction contains only TEMP-table work plus a plan file
-- export; it has no INSERT/UPDATE/DELETE against a persistent table.
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ;
\\i ${CONTAINER_PREFIX}.common.sql
COPY (
  SELECT jsonb_build_object(
           'action', action,
           'business_key', business_key,
           'payload', payload,
           'blocking_reason', blocking_reason
         )::text
  FROM merge_plan
  ORDER BY action, business_key::text, payload::text, coalesce(blocking_reason, '')
) TO :'plan_path';
SELECT concat_ws('|',
  'DP80_METRICS_V1',
	  (SELECT count(*) FROM stage_songs_raw),
	  (SELECT count(*) FROM source_song_keys),
	  (SELECT coalesce(sum(row_count - 1), 0) FROM source_song_keys),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'duplicate source song content key'),
	  (SELECT count(*) FROM stage_artists_raw),
  (SELECT count(*) FROM stage_artists),
  (SELECT count(*) FROM merge_plan WHERE action = 'insert_framework'),
  (SELECT count(*) FROM merge_plan WHERE action = 'insert_label'),
  (SELECT count(*) FROM merge_plan WHERE action = 'insert_artist'),
  (SELECT count(*) FROM merge_plan WHERE action = 'insert_song'),
  (SELECT count(*) FROM merge_plan WHERE action = 'update_variant'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'production variant_of_id differs from the source plan'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'invalid source release_date'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'duplicate production song content key'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'duplicate production artist name'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'duplicate production label name'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'duplicate production framework name'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'variant target is missing from the source snapshot'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'variant points to the same content key'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason IN ('sequence repair requires cache_size=1', 'required sequence is missing')),
	  (SELECT count(*) FROM merge_plan WHERE action = 'preserve_song_difference'),
	  (SELECT count(*) FROM merge_plan WHERE action = 'blocker'),
	  (SELECT count(*) FROM merge_plan WHERE blocking_reason = 'variant chain reaches a cycle'),
	  (SELECT count(*) FROM songs),
  (SELECT count(*) FROM songs) + (SELECT count(*) FROM merge_plan WHERE action = 'insert_song'),
  (SELECT count(*) FROM merge_plan WHERE action = 'sequence_bump')
);
COMMIT;
SQL
    )
    parse_metrics "$metrics"

    docker cp "$DST:${CONTAINER_PREFIX}.plan" "$WORK_DIR/plan.jsonl" >/dev/null
    [[ -f "$WORK_DIR/plan.jsonl" ]] || die "preview plan was not produced"
    local plan_digest
    plan_digest=$(sha256sum "$WORK_DIR/plan.jsonl" | awk '{print $1}')
    is_digest "$plan_digest" || die "cannot calculate plan digest"

    printf '%s\n' \
        "SNAPSHOT_DIGEST=$snapshot_digest" \
        "PLAN_DIGEST=$plan_digest" \
        "SOURCE_SONG_ROWS=$SOURCE_SONG_ROWS" \
        "UNIQUE_SONG_KEYS=$UNIQUE_SONG_KEYS" \
        "DUPLICATE_SOURCE_ROWS=$DUPLICATE_SOURCE_ROWS" \
        "DUPLICATE_SOURCE_KEYS=$DUPLICATE_SOURCE_KEYS" \
        "SOURCE_ARTIST_ROWS=$SOURCE_ARTIST_ROWS" \
        "UNIQUE_ARTISTS=$UNIQUE_ARTISTS" \
        "FRAMEWORKS_INSERT=$FRAMEWORKS_INSERT" \
        "LABELS_INSERT=$LABELS_INSERT" \
        "ARTISTS_INSERT=$ARTISTS_INSERT" \
        "SONGS_INSERT=$SONGS_INSERT" \
        "VARIANT_BACKFILLS=$VARIANT_BACKFILLS" \
        "VARIANT_CONFLICTS=$VARIANT_CONFLICTS" \
        "INVALID_RELEASE_DATES=$INVALID_RELEASE_DATES" \
        "DUPLICATE_PRODUCTION_SONG_KEYS=$DUPLICATE_PRODUCTION_SONG_KEYS" \
        "DUPLICATE_PRODUCTION_ARTIST_NAMES=$DUPLICATE_PRODUCTION_ARTIST_NAMES" \
        "DUPLICATE_PRODUCTION_LABEL_NAMES=$DUPLICATE_PRODUCTION_LABEL_NAMES" \
        "DUPLICATE_PRODUCTION_FRAMEWORK_NAMES=$DUPLICATE_PRODUCTION_FRAMEWORK_NAMES" \
        "MISSING_VARIANT_TARGETS=$MISSING_VARIANT_TARGETS" \
        "SELF_VARIANT_LINKS=$SELF_VARIANT_LINKS" \
        "SEQUENCE_CACHE_BLOCKERS=$SEQUENCE_CACHE_BLOCKERS" \
        "PRESERVED_DIFFERENCES=$PRESERVED_DIFFERENCES" \
        "BLOCKERS=$BLOCKERS" \
        "VARIANT_CYCLES=$VARIANT_CYCLES" \
        "PRODUCTION_SONGS_BEFORE=$PRODUCTION_SONGS_BEFORE" \
        "PRODUCTION_SONGS_AFTER=$PRODUCTION_SONGS_AFTER" \
        "SEQUENCE_BUMPS=$SEQUENCE_BUMPS" >"$WORK_DIR/preview.meta"

    emit_event plan completed "exact plan stored"
    PREVIEW_PERSISTED=yes
    printf 'DP80_PREVIEW_JSON={"schema_version":1,"preview_id":"%s","source_digest":"%s","plan_digest":"%s","source_song_rows":%s,"unique_song_keys":%s,"duplicate_source_rows":%s,"duplicate_source_song_keys":%s,"source_artist_rows":%s,"unique_artists":%s,"frameworks_to_insert":%s,"labels_to_insert":%s,"artists_to_insert":%s,"songs_to_insert":%s,"variants_to_backfill":%s,"variant_conflicts":%s,"invalid_release_dates":%s,"duplicate_production_song_keys":%s,"duplicate_production_artist_names":%s,"duplicate_production_label_names":%s,"duplicate_production_framework_names":%s,"missing_variant_targets":%s,"self_variant_links":%s,"sequence_cache_blockers":%s,"preserved_song_differences":%s,"blockers":%s,"variant_cycles":%s,"production_songs_before":%s,"production_songs_after":%s,"sequence_bumps":%s}\n' \
        "$TOKEN" "$snapshot_digest" "$plan_digest" \
        "$SOURCE_SONG_ROWS" "$UNIQUE_SONG_KEYS" "$DUPLICATE_SOURCE_ROWS" "$DUPLICATE_SOURCE_KEYS" \
        "$SOURCE_ARTIST_ROWS" "$UNIQUE_ARTISTS" "$FRAMEWORKS_INSERT" \
        "$LABELS_INSERT" "$ARTISTS_INSERT" "$SONGS_INSERT" \
        "$VARIANT_BACKFILLS" "$VARIANT_CONFLICTS" "$INVALID_RELEASE_DATES" \
        "$DUPLICATE_PRODUCTION_SONG_KEYS" "$DUPLICATE_PRODUCTION_ARTIST_NAMES" \
        "$DUPLICATE_PRODUCTION_LABEL_NAMES" "$DUPLICATE_PRODUCTION_FRAMEWORK_NAMES" \
        "$MISSING_VARIANT_TARGETS" "$SELF_VARIANT_LINKS" "$SEQUENCE_CACHE_BLOCKERS" \
        "$PRESERVED_DIFFERENCES" "$BLOCKERS" "$VARIANT_CYCLES" "$PRODUCTION_SONGS_BEFORE" \
        "$PRODUCTION_SONGS_AFTER" "$SEQUENCE_BUMPS"
}

load_preview_meta() {
    local meta="$1" key value
    while IFS='=' read -r key value; do
        case "$key" in
            SNAPSHOT_DIGEST) SNAPSHOT_DIGEST="$value" ;;
            PLAN_DIGEST) PLAN_DIGEST="$value" ;;
            SOURCE_SONG_ROWS) SOURCE_SONG_ROWS="$value" ;;
            UNIQUE_SONG_KEYS) UNIQUE_SONG_KEYS="$value" ;;
            DUPLICATE_SOURCE_ROWS) DUPLICATE_SOURCE_ROWS="$value" ;;
            DUPLICATE_SOURCE_KEYS) DUPLICATE_SOURCE_KEYS="$value" ;;
            SOURCE_ARTIST_ROWS) SOURCE_ARTIST_ROWS="$value" ;;
            UNIQUE_ARTISTS) UNIQUE_ARTISTS="$value" ;;
            FRAMEWORKS_INSERT) FRAMEWORKS_INSERT="$value" ;;
            LABELS_INSERT) LABELS_INSERT="$value" ;;
            ARTISTS_INSERT) ARTISTS_INSERT="$value" ;;
            SONGS_INSERT) SONGS_INSERT="$value" ;;
            VARIANT_BACKFILLS) VARIANT_BACKFILLS="$value" ;;
            VARIANT_CONFLICTS) VARIANT_CONFLICTS="$value" ;;
            INVALID_RELEASE_DATES) INVALID_RELEASE_DATES="$value" ;;
            DUPLICATE_PRODUCTION_SONG_KEYS) DUPLICATE_PRODUCTION_SONG_KEYS="$value" ;;
            DUPLICATE_PRODUCTION_ARTIST_NAMES) DUPLICATE_PRODUCTION_ARTIST_NAMES="$value" ;;
            DUPLICATE_PRODUCTION_LABEL_NAMES) DUPLICATE_PRODUCTION_LABEL_NAMES="$value" ;;
            DUPLICATE_PRODUCTION_FRAMEWORK_NAMES) DUPLICATE_PRODUCTION_FRAMEWORK_NAMES="$value" ;;
            MISSING_VARIANT_TARGETS) MISSING_VARIANT_TARGETS="$value" ;;
            SELF_VARIANT_LINKS) SELF_VARIANT_LINKS="$value" ;;
            SEQUENCE_CACHE_BLOCKERS) SEQUENCE_CACHE_BLOCKERS="$value" ;;
            PRESERVED_DIFFERENCES) PRESERVED_DIFFERENCES="$value" ;;
            BLOCKERS) BLOCKERS="$value" ;;
            VARIANT_CYCLES) VARIANT_CYCLES="$value" ;;
            PRODUCTION_SONGS_BEFORE) PRODUCTION_SONGS_BEFORE="$value" ;;
            PRODUCTION_SONGS_AFTER) PRODUCTION_SONGS_AFTER="$value" ;;
            SEQUENCE_BUMPS) SEQUENCE_BUMPS="$value" ;;
            *) die "preview metadata contains an unknown field" ;;
        esac
    done <"$meta"

    is_digest "${SNAPSHOT_DIGEST:-}" || die "invalid stored snapshot digest"
    is_digest "${PLAN_DIGEST:-}" || die "invalid stored plan digest"
    parse_metrics "DP80_METRICS_V1|${SOURCE_SONG_ROWS:-}|${UNIQUE_SONG_KEYS:-}|${DUPLICATE_SOURCE_ROWS:-}|${DUPLICATE_SOURCE_KEYS:-}|${SOURCE_ARTIST_ROWS:-}|${UNIQUE_ARTISTS:-}|${FRAMEWORKS_INSERT:-}|${LABELS_INSERT:-}|${ARTISTS_INSERT:-}|${SONGS_INSERT:-}|${VARIANT_BACKFILLS:-}|${VARIANT_CONFLICTS:-}|${INVALID_RELEASE_DATES:-}|${DUPLICATE_PRODUCTION_SONG_KEYS:-}|${DUPLICATE_PRODUCTION_ARTIST_NAMES:-}|${DUPLICATE_PRODUCTION_LABEL_NAMES:-}|${DUPLICATE_PRODUCTION_FRAMEWORK_NAMES:-}|${MISSING_VARIANT_TARGETS:-}|${SELF_VARIANT_LINKS:-}|${SEQUENCE_CACHE_BLOCKERS:-}|${PRESERVED_DIFFERENCES:-}|${BLOCKERS:-}|${VARIANT_CYCLES:-}|${PRODUCTION_SONGS_BEFORE:-}|${PRODUCTION_SONGS_AFTER:-}|${SEQUENCE_BUMPS:-}"
}

run_apply() {
    [[ "$TOKEN" =~ ^preview-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9]{8}$ ]] || die "invalid preview_id"
    WORK_DIR="$PREVIEW_ROOT/$TOKEN"
    [[ -d "$WORK_DIR" && -f "$WORK_DIR/snapshot.jsonl" && -f "$WORK_DIR/plan.jsonl" && -f "$WORK_DIR/preview.meta" ]] \
        || die "preview does not exist or is incomplete: $TOKEN"
    PREVIEW_LOCK_DIR="$WORK_DIR/operation.lock"
    mkdir -- "$PREVIEW_LOCK_DIR" 2>/dev/null \
        || die "preview is already being applied or discarded: $TOKEN"
    PREVIEW_LOCK_OWNED=yes
    load_preview_meta "$WORK_DIR/preview.meta"
    [[ "$(sha256sum "$WORK_DIR/snapshot.jsonl" | awk '{print $1}')" == "$SNAPSHOT_DIGEST" ]] \
        || die "stored source snapshot digest changed"
    [[ "$(sha256sum "$WORK_DIR/plan.jsonl" | awk '{print $1}')" == "$PLAN_DIGEST" ]] \
        || die "stored exact plan digest changed"
    [[ "$BLOCKERS" == 0 ]] || die "approved preview has $BLOCKERS blocking conflict(s); create a clean preview first"

    emit_event source_check running "checking current source against approved snapshot"
    SOURCE_CHECK_FILE=$(mktemp "$PREVIEW_ROOT/source-check-${TOKEN}-XXXXXXXX")
    export_snapshot "$SOURCE_CHECK_FILE" no
    local current_source_digest
    current_source_digest=$(sha256sum "$SOURCE_CHECK_FILE" | awk '{print $1}')
    is_digest "$current_source_digest" || die "cannot calculate current source digest"
    [[ "$current_source_digest" == "$SNAPSHOT_DIGEST" ]] \
        || die "source database drifted after preview; run preview again"
    rm -f -- "$SOURCE_CHECK_FILE"
    SOURCE_CHECK_FILE=""
    emit_event source_check completed "current source matches approved snapshot"

    CONTAINER_PREFIX="/tmp/dp80-${TOKEN}-apply-$$"
    write_common_sql "$WORK_DIR/common-apply.sql"
    copy_into_destination "$WORK_DIR/snapshot.jsonl" "${CONTAINER_PREFIX}.snapshot"
    copy_into_destination "$WORK_DIR/common-apply.sql" "${CONTAINER_PREFIX}.common.sql"
    copy_into_destination "$WORK_DIR/plan.jsonl" "${CONTAINER_PREFIX}.expected-plan"

    PROMOTION_ID="merge-${TOKEN#preview-}-$(date -u +%Y%m%dT%H%M%SZ)-$$"
    emit_event audit running "creating durable running audit"
    docker exec -i "$DST" psql -X --no-password -q \
        -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 \
        -v promotion_id="$PROMOTION_ID" -v preview_id="$TOKEN" \
        -v source_db="$SRC_DB" -v target_db="$DST_DB" \
        -v snapshot_digest="$SNAPSHOT_DIGEST" -v plan_digest="$PLAN_DIGEST" <<'SQL'
INSERT INTO data_promotion_runs
       (promotion_id, source_environment, source_database, source_dump_sha256,
        source_backup_path, target_database, operator_name, reviewer_name,
        scope_json, source_db, target_db, status, result_json)
VALUES (:'promotion_id', '8082', :'source_db', :'snapshot_digest',
        'dp80-preview://' || :'preview_id', :'target_db', 'jose', 'GeojoLu',
        jsonb_build_object(
          'tables', jsonb_build_array('frameworks', 'labels', 'artists', 'songs'),
          'target_conflict_policy', 'preserve_existing_song_fields',
          'variant_policy', 'backfill_null_only'),
        :'source_db', :'target_db', 'running',
        jsonb_build_object('preview_id', :'preview_id',
                           'source_digest', :'snapshot_digest',
                           'plan_digest', :'plan_digest'));
SQL

    emit_event transaction running "locking production and checking exact plan"
    set +e
    docker exec -i "$DST" psql -X --no-password -qAt \
        -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 \
        -v snapshot_path="${CONTAINER_PREFIX}.snapshot" \
        -v expected_plan_path="${CONTAINER_PREFIX}.expected-plan" \
        -v promotion_id="$PROMOTION_ID" -v preview_id="$TOKEN" \
        -v snapshot_digest="$SNAPSHOT_DIGEST" -v plan_digest="$PLAN_DIGEST" <<SQL
BEGIN;
DO \$lock\$
BEGIN
  IF NOT pg_try_advisory_xact_lock(808280) THEN
    RAISE EXCEPTION 'another dp80 data merge is running' USING ERRCODE = '55P03';
  END IF;
END
\$lock\$;

LOCK TABLE frameworks, labels, artists, songs IN SHARE ROW EXCLUSIVE MODE;
\\i ${CONTAINER_PREFIX}.common.sql

CREATE TEMP TABLE expected_plan_raw(line text) ON COMMIT DROP;
COPY expected_plan_raw(line) FROM :'expected_plan_path';
CREATE TEMP TABLE expected_plan ON COMMIT DROP AS
SELECT payload->>'action' AS action,
       payload->'business_key' AS business_key,
       payload->'payload' AS payload,
       payload->>'blocking_reason' AS blocking_reason
FROM (SELECT line::jsonb AS payload FROM expected_plan_raw) q;

DO \$drift\$
BEGIN
  IF EXISTS (
    (SELECT action, business_key, payload, blocking_reason FROM merge_plan
     EXCEPT ALL
     SELECT action, business_key, payload, blocking_reason FROM expected_plan)
    UNION ALL
    (SELECT action, business_key, payload, blocking_reason FROM expected_plan
     EXCEPT ALL
     SELECT action, business_key, payload, blocking_reason FROM merge_plan)
  ) THEN
    RAISE EXCEPTION 'approved preview drifted; run preview again' USING ERRCODE = '40001';
  END IF;
  IF EXISTS (SELECT 1 FROM merge_plan WHERE action = 'blocker') THEN
    RAISE EXCEPTION 'merge plan contains blocking conflicts' USING ERRCODE = '23514';
  END IF;
END
\$drift\$;

\\echo DP80_EVENT_JSON={"schema_version":1,"phase":"plan_check","status":"completed","message":"exact approved plan matched"}

DO \$sequences\$
DECLARE p record;
BEGIN
  FOR p IN
    SELECT payload->>'sequence_name' AS sequence_name,
           (payload->>'new_value')::bigint AS new_value
    FROM merge_plan WHERE action = 'sequence_bump'
  LOOP
    PERFORM setval(p.sequence_name::regclass, p.new_value, true);
  END LOOP;
END
\$sequences\$;

CREATE TEMP TABLE actual_counts(kind text PRIMARY KEY, value bigint NOT NULL) ON COMMIT DROP;

WITH inserted AS (
  INSERT INTO frameworks(name, description)
  SELECT payload->>'name', NULL
  FROM merge_plan WHERE action = 'insert_framework'
  ORDER BY business_key::text
  RETURNING 1
)
INSERT INTO actual_counts SELECT 'frameworks', count(*) FROM inserted;

WITH inserted AS (
  INSERT INTO labels(name)
  SELECT payload->>'name'
  FROM merge_plan WHERE action = 'insert_label'
  ORDER BY business_key::text
  RETURNING 1
)
INSERT INTO actual_counts SELECT 'labels', count(*) FROM inserted;

WITH inserted AS (
  INSERT INTO artists(name)
  SELECT payload->>'name'
  FROM merge_plan WHERE action = 'insert_artist'
  ORDER BY business_key::text
  RETURNING 1
)
INSERT INTO actual_counts SELECT 'artists', count(*) FROM inserted;

WITH inserted AS (
  INSERT INTO songs(name, label_id, framework_id, artist_id, lyricist, composer,
                    upload_date, release_date, lyrics_url)
  SELECT s.name, l.id, f.id, a.id, s.lyricist, s.composer,
         s.upload_date, s.release_date, s.lyrics_url
  FROM stage_songs s
  JOIN merge_plan p
    ON p.action = 'insert_song'
   AND p.business_key->>'name' IS NOT DISTINCT FROM s.name
   AND p.business_key->>'artist_name' IS NOT DISTINCT FROM s.artist_name
  LEFT JOIN artists a ON a.name IS NOT DISTINCT FROM s.artist_name
  LEFT JOIN labels l ON l.name IS NOT DISTINCT FROM s.label_name
  LEFT JOIN frameworks f ON f.name IS NOT DISTINCT FROM s.framework_name
  ORDER BY p.business_key::text
  RETURNING 1
)
INSERT INTO actual_counts SELECT 'songs', count(*) FROM inserted;

DROP TABLE target_song_keys;
CREATE TEMP TABLE target_song_keys ON COMMIT DROP AS
SELECT jsonb_build_array(s.name, a.name)::text AS song_key,
       s.name, a.name AS artist_name, count(*)::bigint AS row_count,
       min(s.id)::bigint AS target_id
FROM songs s
LEFT JOIN artists a ON a.id = s.artist_id
GROUP BY s.name, a.name;

CREATE INDEX target_song_keys_song_key_idx ON target_song_keys(song_key);

WITH updated AS (
  UPDATE songs child
  SET variant_of_id = parent_key.target_id
  FROM merge_plan p
  JOIN target_song_keys child_key
    ON child_key.song_key = jsonb_build_array(
         p.business_key->>'name', p.business_key->>'artist_name')::text
   AND child_key.row_count = 1
  JOIN target_song_keys parent_key
    ON parent_key.song_key = jsonb_build_array(
         p.payload->>'variant_name', p.payload->>'variant_artist_name')::text
   AND parent_key.row_count = 1
  WHERE p.action = 'update_variant'
    AND child.id = child_key.target_id
    AND child.variant_of_id IS NULL
    AND child.id <> parent_key.target_id
  RETURNING 1
)
INSERT INTO actual_counts SELECT 'variants', count(*) FROM updated;

DO \$counts\$
DECLARE expected bigint; actual bigint;
BEGIN
  FOR expected, actual IN
    SELECT e.expected, coalesce(a.value, 0)
    FROM (VALUES
      ('frameworks', (SELECT count(*) FROM merge_plan WHERE action='insert_framework')),
      ('labels',     (SELECT count(*) FROM merge_plan WHERE action='insert_label')),
      ('artists',    (SELECT count(*) FROM merge_plan WHERE action='insert_artist')),
      ('songs',      (SELECT count(*) FROM merge_plan WHERE action='insert_song')),
      ('variants',   (SELECT count(*) FROM merge_plan WHERE action='update_variant'))
    ) AS e(kind, expected)
    LEFT JOIN actual_counts a USING(kind)
  LOOP
    IF expected <> actual THEN
      RAISE EXCEPTION 'actual merge count % differs from approved count %', actual, expected;
    END IF;
  END LOOP;
END
\$counts\$;

UPDATE data_promotion_runs
SET status = 'completed', completed_at = now(),
    songs_inserted = (SELECT value FROM actual_counts WHERE kind='songs'),
    result_json = coalesce(result_json, '{}'::jsonb) || jsonb_build_object(
      'frameworks_inserted', (SELECT value FROM actual_counts WHERE kind='frameworks'),
      'labels_inserted', (SELECT value FROM actual_counts WHERE kind='labels'),
      'artists_inserted', (SELECT value FROM actual_counts WHERE kind='artists'),
      'songs_inserted', (SELECT value FROM actual_counts WHERE kind='songs'),
      'variants_backfilled', (SELECT value FROM actual_counts WHERE kind='variants'),
      'preserved_song_differences', (SELECT count(*) FROM merge_plan WHERE action='preserve_song_difference'),
      'sequence_bumps', (SELECT count(*) FROM merge_plan WHERE action='sequence_bump'))
WHERE promotion_id = :'promotion_id' AND status = 'running';

\\echo DP80_EVENT_JSON={"schema_version":1,"phase":"transaction","status":"committing","message":"all exact counts matched"}
COMMIT;
SELECT 'DP80_RESULT_JSON=' || jsonb_build_object(
  'schema_version', 1, 'status', 'committed',
  'promotion_id', :'promotion_id', 'preview_id', :'preview_id',
  'source_digest', :'snapshot_digest', 'plan_digest', :'plan_digest')::text;
SQL
    local rc=$?
    set -e
    if (( rc != 0 )); then
        emit_event transaction rolled_back "production business changes were rolled back"
        return "$rc"
    fi

    FINALIZED=yes
    PREVIEW_LOCK_DIR=""
    PREVIEW_LOCK_OWNED=no
    rm -rf -- "$WORK_DIR"
    emit_event result committed "approved plan committed"
}

run_discard() {
    [[ "$TOKEN" =~ ^preview-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9]{8}$ ]] || die "invalid preview_id"
    WORK_DIR="$PREVIEW_ROOT/$TOKEN"
    [[ -d "$WORK_DIR" ]] || die "preview does not exist: $TOKEN"
    PREVIEW_LOCK_DIR="$WORK_DIR/operation.lock"
    mkdir -- "$PREVIEW_LOCK_DIR" 2>/dev/null \
        || die "preview is already being applied or discarded: $TOKEN"
    PREVIEW_LOCK_OWNED=yes
    PREVIEW_LOCK_DIR=""
    PREVIEW_LOCK_OWNED=no
    rm -rf -- "$WORK_DIR"
    emit_event preview discarded "unused preview token removed"
}

case "$MODE" in
    preview)
        [[ $# -eq 1 ]] || die "usage: migrate-songs.sh preview"
        configure_runtime
        require_runtime
        run_preview
        ;;
    apply)
        [[ $# -eq 2 ]] || die "usage: migrate-songs.sh apply <preview_id>"
        configure_runtime
        require_runtime
        run_apply
        ;;
    discard)
        [[ $# -eq 2 ]] || die "usage: migrate-songs.sh discard <preview_id>"
        configure_runtime
        run_discard
        ;;
    *)
        die "usage: migrate-songs.sh preview | apply <preview_id> | discard <preview_id>"
        ;;
esac
