#!/usr/bin/env bash
# deploy-80.sh — Jose · Gandalf 80 端口部署面板
# 仓库: github.com/geojolu/deploy-80
set -euo pipefail

DEPLOY_TOOL="/Users/geojol/Documents/Projects/datacenter-kimi/scripts/release/production-80.sh"
SSH_HOST="gandalf"
PROD_DIR="/home/gandalf/projects/datacenter-kimi-production"
RELEASE_ROOT="/home/gandalf/releases/datacenter-kimi"

# ── ANSI ──────────────────────────────────────────────────────────────
BOLD='\033[1m';    OFF='\033[0m'
GREEN='\033[32m';  YELLOW='\033[33m';  RED='\033[31m';  CYAN='\033[36m';  DIM='\033[2m'

say()  { echo -e "  ${GREEN}->${OFF} $*"; }
warn() { echo -e "  ${YELLOW}[!]${OFF} $*"; }
_ssh() { ssh -o BatchMode=yes "$SSH_HOST" "$@" </dev/null 2>/dev/null; }

# ── 数据获取（并行）───────────────────────────────────────────────────
fetch_all() {
    # 所有远程数据在一个 ssh 连接里拿完
    _ssh "
        echo '===CONTAINERS==='
        docker compose -p datacenter-kimi-production ps --format '{{.Service}} {{.State}} {{.Status}}'
        echo '===LIVEZ==='
        curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/api/livez
        echo ''
        echo '===RELEASE_ENV==='
        cat $PROD_DIR/release.env
        echo '===GIT==='
        git -C $PROD_DIR log --oneline -1
        git -C $PROD_DIR status --porcelain --untracked-files=no | wc -l | tr -d ' '
        echo '===DATA_80==='
        docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM songs;'
        docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM artists;'
        echo '===DATA_82==='
        docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM songs;' 2>/dev/null || echo '?'
        docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM artists;' 2>/dev/null || echo '?'
        echo '===RELEASES==='
        ls -1t $RELEASE_ROOT/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5
    "
}

# ── 解析远程输出 ─────────────────────────────────────────────────────
parse_remote() {
    local mode=""
    while IFS= read -r line; do
        case "$line" in
            ===CONTAINERS===) mode="containers" ;;
            ===LIVEZ===)      mode="livez" ;;
            ===RELEASE_ENV===) mode="release_env" ;;
            ===GIT===)        mode="git" ;;
            ===DATA_80===)    mode="data80" ;;
            ===DATA_82===)    mode="data82" ;;
            ===RELEASES===)   mode="releases" ;;
            *)
                case "$mode" in
                    containers)
                        local svc state status
                        read -r svc state status <<< "$line"
                        case "$svc" in
                            backend)   BE_ST="${state} ${status}" ;;
                            scheduler) SC_ST="${state} ${status}" ;;
                            proxy)     PX_ST="${state} ${status}" ;;
                            db)        DB_ST="${state} ${status}" ;;
                        esac
                        ;;
                    livez)
                        LOCAL_LIVEZ="$line"
                        ;;
                    release_env)
                        case "$line" in
                            KIMI_BACKEND_IMAGE=*) BE_IMG="${line#*=}" ;;
                            KIMI_WEB_IMAGE=*)     WE_IMG="${line#*=}" ;;
                        esac
                        ;;
                    git)
                        if [[ -z "${GIT_HEAD:-}" ]]; then
                            GIT_HEAD="$line"
                        else
                            GIT_DIRTY="$line"
                        fi
                        ;;
                    data80)
                        if [[ -z "${S80:-}" ]]; then S80="$line"; else A80="$line"; fi
                        ;;
                    data82)
                        if [[ -z "${S82:-}" ]]; then S82="$line"; else A82="$line"; fi
                        ;;
                    releases)
                        RELEASES+=("$line")
                        ;;
                esac
                ;;
        esac
    done
}

# ── 画板 ──────────────────────────────────────────────────────────────
draw() {
    # 重置缓存变量
    BE_ST=""; SC_ST=""; PX_ST=""; DB_ST=""
    LOCAL_LIVEZ=""; BE_IMG=""; WE_IMG=""
    GIT_HEAD=""; GIT_DIRTY=""
    S80=""; A80=""; S82=""; A82=""
    RELEASES=()

    local raw
    raw=$(fetch_all)
    parse_remote <<< "$raw"

    # 探活（公网从本地测）
    local public_livez
    public_livez=$(curl -s -o /dev/null -w "%{http_code}" https://gandalf.zebra-diminished.ts.net/api/livez 2>/dev/null)

    # ── 渲染 ──────────────────────────────────────────────────────────
    cat <<EOF

  ${BOLD}${CYAN}╔════════════════════════════════════════════════╗
  ║     Jose · 80 Port Deploy Panel               ║
  ╚════════════════════════════════════════════════╝${OFF}

  ${BOLD}CONTAINERS${OFF}
EOF

    _ct() {
        local s="$1"
        [[ "$s" == *"healthy"* ]] && echo -e "${GREEN}${s}${OFF}" || echo -e "${RED}${s}${OFF}"
    }
    printf "    %-12s %s\n" "backend"   "$(_ct "$BE_ST")"
    printf "    %-12s %s\n" "scheduler" "$(_ct "$SC_ST")"
    printf "    %-12s %s\n" "proxy"     "$(_ct "$PX_ST")"
    printf "    %-12s %s\n" "db"        "$(_ct "$DB_ST")"

    # ── 探活 ──
    local lz_icon pz_icon
    [[ "$LOCAL_LIVEZ"  == "200" ]] && lz_icon="${GREEN}OK${OFF}"  || lz_icon="${RED}${LOCAL_LIVEZ}${OFF}"
    [[ "$public_livez"  == "200" ]] && pz_icon="${GREEN}OK${OFF}"  || pz_icon="${RED}${public_livez}${OFF}"
    echo ""
    echo -e "  ${BOLD}HEALTH${OFF}       local: ${lz_icon}    public: ${pz_icon}"

    # ── 镜像 ──
    echo ""
    echo -e "  ${BOLD}IMAGES${OFF}       backend  ${DIM}${BE_IMG:0:19}${OFF}"
    echo -e "               web      ${DIM}${WE_IMG:0:19}${OFF}"

    # ── 数据 80 vs 8082 ──
    local s_diff a_diff promote_hint=""
    s_diff=$(( S82 - S80 )) 2>/dev/null || s_diff=0
    a_diff=$(( A82 - A80 )) 2>/dev/null || a_diff=0
    [[ "$s_diff" -gt 0 ]] && promote_hint=" ${YELLOW}<- 8082 has +${s_diff} songs${OFF}"

    echo ""
    echo -e "  ${BOLD}DATA${OFF}       80 / 8082"
    printf "    %-12s ${BOLD}%-7s${OFF}  %-7s  ${DIM}diff${OFF}\n" "songs" "$S80" "$S82"
    printf "              ${DIM}%-7s${OFF}  %-7s  %s\n" "" "" "$s_diff$promote_hint"
    printf "    %-12s ${BOLD}%-7s${OFF}  %-7s\n" "artists" "$A80" "$A82"
    printf "              ${DIM}%-7s${OFF}  %-7s  %s\n" "" "" "$a_diff"

    # ── Git ──
    echo ""
    echo -e "  ${BOLD}GIT${OFF}          HEAD  ${DIM}${GIT_HEAD:0:52}${OFF}"
    if [[ "${GIT_DIRTY:-0}" != "0" ]]; then
        echo -e "               repo  ${YELLOW}${GIT_DIRTY} files dirty${OFF}  ${DIM}-> type 'clean'${OFF}"
    else
        echo -e "               repo  ${GREEN}clean${OFF}"
    fi

    # ── 最近 releases ──
    echo ""
    echo -e "  ${BOLD}RELEASES${OFF}"
    if [[ ${#RELEASES[@]} -eq 0 ]]; then
        echo "    (none)"
    else
        for rid in "${RELEASES[@]}"; do
            [[ -z "$rid" ]] && continue
            local ci
            ci=$(_ssh "python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$rid/release.json\")); print(m.get(\"release\",{}).get(\"commit\",\"?\")[:8])' 2>/dev/null" || echo "?")
            printf "    ${DIM}%-42s${OFF} %s\n" "$rid" "$ci"
        done
    fi

    # ── 命令栏 ──
    cat <<EOF

  ${BOLD}${CYAN}──────────────────────────────────────────────────${OFF}
  ${BOLD}d${OFF})eploy   ${BOLD}v${OFF})erify   ${BOLD}b${OFF})rowser   ${BOLD}r${OFF})ollback   ${BOLD}rc${OFF})ecover
  ${BOLD}promote${OFF}   ${BOLD}clean${OFF}     ${BOLD}l${OFF})ogs      ${BOLD}i${OFF})nspect    ${BOLD}q${OFF})uit
EOF

    # ── 智能提示 ──
    local hints=()
    [[ "${GIT_DIRTY:-0}" != "0" ]] && hints+=("repo dirty — run ${BOLD}clean${OFF}")
    [[ "$s_diff" -gt 0 ]] && hints+=("8082 ahead by ${s_diff} songs — run ${BOLD}promote${OFF}")

    if [[ ${#hints[@]} -gt 0 ]]; then
        echo ""
        echo -e "  ${YELLOW}${BOLD}→${OFF} ${YELLOW}$(IFS=' | '; echo "${hints[*]}")${OFF}"
    fi
    echo ""
}

# ── 动作函数 ──────────────────────────────────────────────────────────
do_deploy() {
    echo ""
    _ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | grep -v '\.lock' | head -8" | while read -r rid; do
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
    echo -e "  ${RED}code/images only — DB is NOT restored${OFF}"
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
    echo -e "  ${BOLD}DB Promotion: 8082 -> 80${OFF}"
    echo "  dry-run)  preview only"
    echo "  go)       execute"
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

pause() { echo ""; echo -n "  [Enter] "; read -r _; }

case "${1:-}" in
    status) draw; exit 0 ;;
    *)      main ;;
esac
