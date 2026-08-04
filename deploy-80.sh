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

# ── 画板 ─────────────────────────────────────────────────────────────
draw() {
    local raw livez be_img we_img git_head git_dirty s80 a80 s82 a82
    local be_st sc_st px_st db_st releases_str

    raw=$(_ssh "
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
        ls -1t $RELEASE_ROOT/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5
    ")

    # ── 解析 ──────────────────────────────────────────────────────────
    local mode="" line svc state status
    while IFS= read -r line; do
        case "$line" in
            ---LIVEZ---)   mode="livez"; continue ;;
            ---ENV---)     mode="env"; continue ;;
            ---GIT---)     mode="git"; continue ;;
            ---DATA80---)  mode="data80"; continue ;;
            ---DATA82---)  mode="data82"; continue ;;
            ---RELEASES---) mode="releases"; continue ;;
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
                releases_str+="    ${line}"$'\n' ;;
            *)
                read -r svc state status <<< "$line" 2>/dev/null || true
                case "$svc" in
                    backend)   be_st="${state} ${status}" ;;
                    scheduler) sc_st="${state} ${status}" ;;
                    proxy)     px_st="${state} ${status}" ;;
                    db)        db_st="${state} ${status}" ;;
                esac ;;
        esac
    done <<< "$raw"

    local pub_livez
    pub_livez=$(curl -s -o /dev/null -w "%{http_code}" https://gandalf.zebra-diminished.ts.net/api/livez 2>/dev/null)

    # ── 状态标记 ──
    _ok()  { [[ "$1" == *"healthy"* ]] && echo "[OK]" || echo "[DOWN]"; }
    _lz()  { [[ "$1" == "200" ]] && echo "OK" || echo "FAIL($1)"; }
    _img() { echo "${1:0:19}"; }

    local s_diff a_diff
    s_diff=$(( ${s82:-0} - ${s80:-0} )) 2>/dev/null || s_diff=0
    a_diff=$(( ${a82:-0} - ${a80:-0} )) 2>/dev/null || a_diff=0

    # ── 渲染 ──────────────────────────────────────────────────────────
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
    backend     $(_img "$be_img")
    web         $(_img "$we_img")

  DATA          80         8082       diff
    songs       ${s80:-?}         ${s82:-?}         $s_diff
    artists     ${a80:-?}         ${a82:-?}         $a_diff
EOF

    if [[ "$s_diff" -gt 0 ]]; then
        echo "               *** 8082 ahead by $s_diff songs -- run: promote"
    fi

    cat <<EOF

  GIT
    HEAD        ${git_head:0:52}
    repo        $([ "${git_dirty:-1}" != "0" ] && echo "DIRTY (${git_dirty} files)" || echo "clean")
EOF

    if [[ "${git_dirty:-1}" != "0" ]]; then
        echo "               *** repo dirty -- run: clean"
    fi

    echo ""
    echo "  RELEASES"
    if [[ -z "${releases_str:-}" ]]; then
        echo "    (none)"
    else
        echo -n "$releases_str"
    fi

    cat <<EOF

  ====================================================
  d)eploy   v)erify   b)rowser   r)ollback   rc)ecover
  promote   clean     l)ogs      i)nspect    q)uit
  ====================================================
EOF

    # ── 智能提示汇总 ──
    local hints=""
    [[ "${git_dirty:-0}" != "0" ]] && hints="$hints repo-dirty"
    [[ "$s_diff" -gt 0 ]] && hints="$hints 8082-ahead-by-$s_diff"
    if [[ -n "$hints" ]]; then
        echo "  --> action needed:$hints"
    fi
    echo ""
}

# ── 动作函数 ──────────────────────────────────────────────────────────
do_deploy() {
    echo ""
    _ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -8" | while read -r rid; do
        local info
        info=$(_ssh "python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$rid/release.json\")); r=m[\"release\"]; print(f\"{r[\"commit\"][:12]}  {r[\"backend_version\"]}  {r[\"migration_class\"]}\")' 2>/dev/null" || echo "?")
        echo "  $rid  $info"
    done
    echo ""
    echo -n "  release.json path: "
    read -r mp
    [[ -z "$mp" ]] && { warn "cancelled"; return; }
    [[ ! -f "$mp" ]] && { echo "  not found"; return; }
    echo -n "  confirm deploy? [y/N] "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" apply "$mp" --execute --allow-port80-downtime 2>&1
}

do_verify() {
    echo -n "  release.json path: "; read -r mp
    [[ -z "$mp" ]] && { warn "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" verify "$mp" 2>&1
}

do_browser() {
    echo -n "  release.json path: "; read -r mp
    echo -n "  evidence.json path: "; read -r ep
    [[ -z "$mp" || -z "$ep" ]] && { warn "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" complete-browser "$mp" --evidence "$ep" --execute 2>&1
}

do_rollback() {
    echo -n "  release.json path: "; read -r mp
    [[ -z "$mp" ]] && { warn "cancelled"; return; }
    echo "  *** code/images only -- DB is NOT restored ***"
    echo -n "  confirm? [y/N] "; read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" rollback-code "$mp" --execute --approved-by GeojoLu 2>&1
}

do_recover() {
    echo -n "  release.json path: "; read -r mp
    [[ -z "$mp" ]] && { warn "cancelled"; return; }
    echo -n "  confirm? [y/N] "; read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" recover-interrupted "$mp" --execute --approved-by GeojoLu 2>&1
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

do_logs() {
    echo -n "  service [b/s/p/d]: "; read -r svc
    case "$svc" in
        b) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 backend" ;;
        s) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 scheduler" ;;
        p) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 proxy" ;;
        d) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 db" ;;
        *) warn "cancelled" ;;
    esac
}

do_promote() {
    echo ""
    echo "  DB Promotion: 8082 -> 80"
    echo "  dry-run) preview only"
    echo "  go)      execute"
    echo -n "  choice: "; read -r choice
    case "$choice" in
        dry-run) _ssh "bash /home/gandalf/projects/datacenter-kimi/scripts/promote-db-8082-to-80.sh --dry-run" ;;
        go)
            echo -n "  confirm? [y/N] "; read -r REPLY
            [[ "$REPLY" =~ ^[Yy]$ ]] && ssh -t -o BatchMode=yes "$SSH_HOST" \
                "bash /home/gandalf/projects/datacenter-kimi/scripts/promote-db-8082-to-80.sh --yes" </dev/null 2>/dev/null
            ;;
        *) warn "cancelled" ;;
    esac
}

do_inspect() {
    say "running inspect..."
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" inspect 2>&1 || warn "inspect failed"
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
            b)       do_browser;  pause ;;
            r)       do_rollback; pause ;;
            rc)      do_recover;  pause ;;
            clean)   do_clean;    pause ;;
            promote) do_promote;  pause ;;
            l)       do_logs;     pause ;;
            i)       do_inspect;  pause ;;
            q)       exit 0 ;;
            "")      ;;
            *)       warn "unknown: $choice"; sleep 1 ;;
        esac
    done
}

case "${1:-}" in
    status) draw; exit 0 ;;
    *)      main ;;
esac
