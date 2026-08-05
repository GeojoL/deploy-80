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
echo "[INFO] songs before: $BEFORE"

# Write SQL to a temp file on gandalf then pipe it in to avoid quoting hell
cat > /tmp/migrate_songs_inner.sql << ENDSQL
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
echo "[INFO] INSERT done"

AFTER=$(docker exec "$DST" psql -U "$PG_USER" -d "$DST_DB" -tAc "SELECT count(*) FROM songs;" | tr -d ' ')
echo "[INFO] songs after: $AFTER (inserted $((AFTER - BEFORE)))"
