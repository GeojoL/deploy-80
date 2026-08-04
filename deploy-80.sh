#!/usr/bin/env bash
# deploy-80.sh — Jose · Gandalf 80 Port Deploy Panel
# repo: github.com/GeojoL/deploy-80
set -euo pipefail

DEPLOY_TOOL="/Users/geojol/Documents/Projects/datacenter-kimi/scripts/release/production-80.sh"
SSH_HOST="gandalf"
PROD_DIR="/home/gandalf/projects/datacenter-kimi-production"
RELEASE_ROOT="/home/gandalf/releases/datacenter-kimi"

say()  { echo "  -> $*"; }
warn() { echo "  [!] $*"; }
_ssh() { ssh -o BatchMode=yes "$SSH_HOST" "$@" </dev/null 2>/dev/null; }

# ── 数据获取 ──────────────────────────────────────────────────────────
fetch_data() {
    _ssh "
        docker compose -p datacenter-kimi-production ps --format '{{.Service}} {{.State}} {{.Status}}'
        echo '---LIVEZ---'
        curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/api/livez
        echo ''
        echo '---ENV---'
        cat $PROD_DIR/release.env
        echo '---GIT---'
        git -C $PROD_DIR log --oneline -1
        git -C $PROD_DIR status --porcelain --untracked-files=no | wc -l | tr -d ' '
        echo '---DATA80---'
        docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM songs;'
        docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM artists;'
        echo '---DATA82---'
        docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM songs;' 2>/dev/null || echo '?'
        docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM artists;' 2>/dev/null || echo '?'
        echo '---RELEASES---'
        for d in \$(ls -1t $RELEASE_ROOT/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5); do
            if [ -f '$RELEASE_ROOT/'\$d'/release.json' ]; then
                echo \"RELEASE:\$d:\$(python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/'\$d'/release.json\")); print(m.get(\"release\",{}).get(\"commit\",\"?\")[:8])' 2>/dev/null || echo '?')\"
            else
                echo \"RELEASE:\$d:no-manifest\"
            fi
        done
        echo '---END---'
    "
}

# ── 画板 ─────────────────────────────────────────────────────────────
draw() {
    local raw livez pub_livez be_img we_img git_head git_dirty s80 a80 s82 a82
    local be_st sc_st px_st db_st
    local -a releases=()

    raw=$(fetch_data)
    pub_livez=$(curl -s -o /dev/null -w "%{http_code}" https://gandalf.zebra-diminished.ts.net/api/livez 2>/dev/null)

    local mode=""
    local line_count=0
    while IFS= read -r line; do
        case "$line" in
            ---LIVEZ---)   mode="livez"; continue ;;
            ---ENV---)     mode="env"; continue ;;
            ---GIT---)     mode="git"; continue ;;
            ---DATA80---)  mode="data80"; continue ;;
            ---DATA82---)  mode="data82"; continue ;;
            ---RELEASES---) mode="releases"; continue ;;
            ---END---)     break ;;
        esac
        case "$mode" in
            livez) livez="$line" ;;
            env)
                case "$line" in
                    KIMI_BACKEND_IMAGE=*) be_img="${line#*=}" ;;
                    KIMI_WEB_IMAGE=*)     we_img="${line#*=}" ;;
                esac ;;
            git)
                if [[ -z "${git_head:-}" ]]; then git_head="$line"
                else git_dirty="$line"; fi ;;
            data80)
                if [[ -z "${s80:-}" ]]; then s80="$line"; else a80="$line"; fi ;;
            data82)
                if [[ -z "${s82:-}" ]]; then s82="$line"; else a82="$line"; fi ;;
            releases)
                [[ "$line" == RELEASE:* ]] && releases+=("${line#RELEASE:}") ;;
            *)
                local svc state status
                read -r svc state status <<< "$line" 2>/dev/null || true
                case "$svc" in
                    backend)   be_st="${state} ${status}" ;;
                    scheduler) sc_st="${state} ${status}" ;;
                    proxy)     px_st="${state} ${status}" ;;
                    db)        db_st="${state} ${status}" ;;
                esac ;;
        esac
    done <<< "$raw"

    _ok()  { [[ "$1" == *"healthy"* ]] && echo "[OK]" || echo "[DOWN]"; }
    _lz()  { [[ "$1" == "200" ]] && echo "OK" || echo "FAIL($1)"; }
    local s_diff=$(( ${s82:-0} - ${s80:-0} )) 2>/dev/null || true
    local a_diff=$(( ${a82:-0} - ${a80:-0} )) 2>/dev/null || true

    cat <<EOF

  +----------------------------------------------------+
  |       Jose · 80 Port Deploy Panel                  |
  +----------------------------------------------------+

  CONTAINERS
    backend     $(_ok "$be_st")  $be_st
    scheduler   $(_ok "$sc_st")  $sc_st
    proxy       $(_ok "$px_st")  $px_st
    db          $(_ok "$db_st")  $db_st

  HEALTH
    local       $(_lz "$livez")
    public      $(_lz "$pub_livez")

  IMAGES
    backend     ${be_img:0:19}
    web         ${we_img:0:19}

  DATA          80         8082       diff
    songs       ${s80:-?}         ${s82:-?}         $s_diff
    artists     ${a80:-?}         ${a82:-?}         $a_diff

  GIT
    HEAD        ${git_head:0:52}
    repo        $([ "${git_dirty:-1}" != "0" ] && echo "DIRTY (${git_dirty} files)" || echo "clean")

  RELEASES
EOF

    local i=1
    for r in "${releases[@]}"; do
        local rid="${r%%:*}" ci="${r##*:}"
        printf "    %d) %-45s %s\n" "$i" "$rid" "$ci"
        ((i++))
    done

    cat <<EOF

  ====================================================
EOF

    # hints
    [[ "${git_dirty:-0}" != "0" ]] && echo "  *** repo dirty -- type 'clean'"
    [[ "$s_diff" -gt 0 ]] && echo "  *** 8082 ahead by $s_diff songs -- type 'promote'"

    cat <<EOF
  ====================================================
  d)eploy   v)erify   r)ollback   clean   promote   l)ogs   q)uit
EOF
    echo ""
}

# ── 动作 ─────────────────────────────────────────────────────────────
do_deploy() {
    # 找第一个有 manifest 的 release
    local latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | grep -v '\.lock' | head -10 | while read d; do [ -f $RELEASE_ROOT/\$d/release.json ] && echo \$d && break; done")
    if [[ -z "$latest" ]]; then
        warn "no valid release found"
        return
    fi
    echo ""
    local info
    info=$(_ssh "python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$latest/release.json\")); r=m[\"release\"]; print(f\"commit={r[\"commit\"][:12]}  branch={r[\"candidate_branch\"]}  v{r[\"backend_version\"]}  migration={r[\"migration_class\"]}\")'")
    echo "  latest: $latest"
    echo "  $info"
    echo ""
    echo -n "  deploy this release? [y/N] "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }

    local manifest="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    if [[ ! -f "$manifest" ]]; then
        warn "local manifest not found: $manifest"
        echo -n "  path to release.json: "
        read -r manifest
        [[ -z "$manifest" || ! -f "$manifest" ]] && { warn "not found"; return; }
    fi
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" apply "$manifest" --execute --allow-port80-downtime 2>&1
}

do_verify() {
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    local latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -1")
    local manifest="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    [[ -f "$manifest" ]] || { echo -n "  manifest path: "; read -r manifest; }
    python3 "$DEPLOY_TOOL" verify "$manifest" 2>&1
}

do_rollback() {
    local latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -1")
    local manifest="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    [[ -f "$manifest" ]] || { echo -n "  manifest path: "; read -r manifest; }
    echo "  *** code/images only -- DB is NOT restored ***"
    echo -n "  confirm? [y/N] "; read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" rollback-code "$manifest" --execute --approved-by GeojoLu 2>&1
}

do_clean() {
    local dirty
    dirty=$(_ssh "git -C $PROD_DIR status --porcelain")
    if [[ -z "$dirty" ]]; then
        say "repo is clean"
        return
    fi
    echo "$dirty"
    echo -n "  git checkout -- . && git clean -fd? [y/N] "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    _ssh "cd $PROD_DIR && git checkout -- . && git clean -fd"
    say "cleaned"
}

do_promote() {
    echo ""
    echo -n "  dry-run or go? [d/g]: "
    read -r choice
    case "$choice" in
        d) _ssh "bash /home/gandalf/projects/datacenter-kimi/scripts/promote-db-8082-to-80.sh --dry-run" ;;
        g) echo -n "  confirm? [y/N] "; read -r REPLY
           [[ "$REPLY" =~ ^[Yy]$ ]] && ssh -t -o BatchMode=yes "$SSH_HOST" \
               "bash /home/gandalf/projects/datacenter-kimi/scripts/promote-db-8082-to-80.sh --yes" </dev/null 2>/dev/null ;;
        *) warn "cancelled" ;;
    esac
}

do_logs() {
    echo -n "  service [b/s/p/d]: "; read -r svc
    case "$svc" in
        b) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 backend" ;;
        s) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 scheduler" ;;
        p) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 proxy" ;;
        d) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 db" ;;
    esac
}

pause() { echo ""; echo -n "  [Enter] "; read -r _; }

# ── 主循环 ───────────────────────────────────────────────────────────
main() {
    while true; do
        draw
        echo -n "  > "
        read -r choice || { echo ""; exit 0; }

        case "$choice" in
            d)       do_deploy;   pause ;;
            v)       do_verify;   pause ;;
            r)       do_rollback; pause ;;
            clean)   do_clean;    pause ;;
            promote) do_promote;  pause ;;
            l)       do_logs;     pause ;;
            q)       exit 0 ;;
            "")      ;;
            *)       warn "unknown: $choice" ;;
        esac
    done
}

case "${1:-}" in
    status) draw; exit 0 ;;
    *)      main ;;
esac
