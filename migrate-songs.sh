#!/usr/bin/env bash
set -euo pipefail

SRC=datacenter-kimi-db-1
DST=datacenter-kimi-production-db-1
PG_USER=datacenter
SRC_DB=datacenter_kimi_test
DST_DB=datacenter_kimi_production
SRC_PASS=jUAAvKON20R4CXtui3AUFnSSnsJLF9T1
NET=migrate-$(date +%s)

cleanup() {
    docker network disconnect "$NET" "$SRC" 2>/dev/null || true
    docker network disconnect "$NET" "$DST" 2>/dev/null || true
    docker network rm "$NET" 2>/dev/null || true
}
trap cleanup EXIT

docker network create "$NET" >/dev/null 2>&1
docker network connect "$NET" "$SRC" 2>/dev/null
docker network connect "$NET" "$DST" 2>/dev/null

SRC_IP=$(docker network inspect "$NET" --format '{{range .Containers}}{{if eq .Name "datacenter-kimi-db-1"}}{{.IPv4Address}}{{end}}{{end}}' | cut -d/ -f1)
echo "[INFO] SRC_IP=$SRC_IP on $NET"

BEFORE=$(docker exec "$DST" psql -U "$PG_USER" -d "$DST_DB" -tAc "SELECT count(*) FROM songs;" | tr -d ' ')
PROMOTION_ID="migrate-8082-to-80-$(date +%Y%m%d_%H%M%S)"
echo "[INFO] songs before: $BEFORE"
echo "[INFO] promotion_id: $PROMOTION_ID"

# Write SQL to a temp file on gandalf then pipe it in to avoid quoting hell
cat > /tmp/migrate_songs_inner.sql << ENDSQL
INSERT INTO data_promotion_runs (promotion_id, source_db, target_db, status)
  VALUES ('$PROMOTION_ID', '$SRC_DB', '$DST_DB', 'running')
  ON CONFLICT (promotion_id) DO UPDATE SET status = 'running';

INSERT INTO songs (id, name, label_id, framework_id, artist_id, lyricist, composer,
                   upload_date, release_date, lyrics_url, merged_into_id, variant_of_id)
SELECT s.id, s.name, s.label_id, s.framework_id, s.artist_id, s.lyricist, s.composer,
       s.upload_date, s.release_date::date, s.lyrics_url, s.merged_into_id, s.variant_of_id
FROM dblink(
  'host=SRC_IP_PLACEHOLDER user=PG_USER_PLACEHOLDER password=SRC_PASS_PLACEHOLDER dbname=SRC_DB_PLACEHOLDER',
  'SELECT id, name, label_id, framework_id, artist_id, lyricist, composer,
          upload_date, release_date, lyrics_url, merged_into_id, variant_of_id FROM songs'
) AS s(id integer, name text, label_id integer, framework_id integer, artist_id integer,
       lyricist text, composer text, upload_date timestamptz, release_date text,
       lyrics_url text, merged_into_id integer, variant_of_id integer)
WHERE NOT EXISTS (SELECT 1 FROM songs t WHERE t.id = s.id)
  AND (s.artist_id IS NULL OR EXISTS (SELECT 1 FROM artists a WHERE a.id = s.artist_id))
  AND (s.label_id IS NULL OR EXISTS (SELECT 1 FROM labels l WHERE l.id = s.label_id))
  AND (s.framework_id IS NULL OR EXISTS (SELECT 1 FROM frameworks f WHERE f.id = s.framework_id));
ENDSQL

# Substitute actual values
sed -i \
    -e "s/SRC_IP_PLACEHOLDER/$SRC_IP/g" \
    -e "s/PG_USER_PLACEHOLDER/$PG_USER/g" \
    -e "s/SRC_PASS_PLACEHOLDER/$SRC_PASS/g" \
    -e "s/SRC_DB_PLACEHOLDER/$SRC_DB/g" \
    /tmp/migrate_songs_inner.sql

echo "[INFO] running INSERT..."
docker exec -i "$DST" psql -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 < /tmp/migrate_songs_inner.sql
INSERT_EXIT=$?
echo "[INFO] INSERT done (exit code: $INSERT_EXIT)"

AFTER=$(docker exec "$DST" psql -U "$PG_USER" -d "$DST_DB" -tAc "SELECT count(*) FROM songs;" | tr -d ' ')
INSERTED=$((AFTER - BEFORE))
echo "[INFO] songs after: $AFTER (inserted $INSERTED)"

# 更新审计记录
docker exec "$DST" psql -U "$PG_USER" -d "$DST_DB" -v ON_ERROR_STOP=1 << ENDSQL 2>/dev/null
UPDATE data_promotion_runs
SET status = CASE WHEN $INSERT_EXIT = 0 THEN 'completed' ELSE 'failed' END,
    completed_at = now(),
    songs_inserted = $INSERTED
WHERE promotion_id = '$PROMOTION_ID';
ENDSQL
echo "[AUDIT] Updated promotion record: $PROMOTION_ID (inserted=$INSERTED, status=$([ $INSERT_EXIT -eq 0 ] && echo completed || echo failed))"
