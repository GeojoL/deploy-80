#!/usr/bin/env bash
# deploy-80.sh — Jose · Gandalf :80 Deploy Panel   alias: dp80
set -euo pipefail

DEPLOY_TOOL="/Users/geojol/Documents/Projects/datacenter-kimi/scripts/release/production-80.sh"
LOCAL_REPO="/Users/geojol/Documents/Projects/datacenter-kimi"
SSH_HOST="gandalf"
PROD_DIR="/home/gandalf/projects/datacenter-kimi-production"
RELEASE_ROOT="/home/gandalf/releases/datacenter-kimi"

# ── color mode ────────────────────────────────────────────────────────
# Full color+unicode: real interactive non-tmux terminal only
if [ -t 1 ] && [ -z "${CLAUDECODE:-}" ] && [ -z "${TMUX:-}" ] && [ "${1:-}" != "--plain" ]; then
    R="\033[0m"   B="\033[1m"   DIM="\033[2m"
    GR="\033[32m" YL="\033[33m" RD="\033[31m" CY="\033[36m" MG="\033[35m" BL="\033[34m"
    TL='╭' TR='╮' BL_='╰' BR='╯' HZ='─' VT='│' BULLET='●'
else
    R="" B="" DIM="" GR="" YL="" RD="" CY="" MG="" BL=""
    TL='+' TR='+' BL_='+' BR='+' HZ='-' VT='|' BULLET='*'
fi

say()  { echo -e "  ${GR}→${R} $*"; }
warn() { echo -e "  ${YL}!${R} $*"; }
err()  { echo -e "  ${RD}✗${R} $*"; }
_ssh() { ssh -o BatchMode=yes "$SSH_HOST" "$@" </dev/null 2>/dev/null; }

# ── fetch helpers ─────────────────────────────────────────────────────
_containers() { _ssh "docker compose -p datacenter-kimi-production ps --format '{{.Service}} {{.State}} {{.Status}}' 2>/dev/null"; }
_livez_local() { _ssh "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/api/livez 2>/dev/null"; }
_livez_pub()   { curl -s -o /dev/null -w "%{http_code}" https://gandalf.zebra-diminished.ts.net/api/livez 2>/dev/null; }
_env()         { _ssh "cat $PROD_DIR/release.env 2>/dev/null"; }
_git_log()     { _ssh "git -C $PROD_DIR log --oneline -1 2>/dev/null"; }
_data80()      { _ssh "docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null"; }
_data82()      { _ssh "docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null || echo -e '?\n?'"; }
_releases()    { _ssh "ls -1t $RELEASE_ROOT/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5"; }
_rel_commit()  {
    _ssh "[ -f $RELEASE_ROOT/$1/release.json ] \
        && python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$1/release.json\")); print(m.get(\"release\",{}).get(\"commit\",\"?\")[:8])' 2>/dev/null \
        || echo '--'"
}
_latest_release() {
    _ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -10 | while read d; do [ -f $RELEASE_ROOT/\$d/release.json ] && echo \$d && break; done"
}
_local_manifest() { echo "$LOCAL_REPO/release-manifest/production-80/$1/release.json"; }

# ── render helpers ────────────────────────────────────────────────────
_st() {   # container status → colored string
    if   [[ "$1" == *"healthy"* ]]; then echo -e "${GR}${B}${BULLET}${R} ${GR}$1${R}"
    elif [[ "$1" == *"Up"*      ]]; then echo -e "${GR}${BULLET}${R} ${GR}$1${R}"
    elif [[ -z "$1"             ]]; then echo -e "${DIM}--${R}"
    else                                 echo -e "${RD}${B}${BULLET}${R} ${RD}$1${R}"
    fi
}
_lz() {   # http code → colored
    [[ "$1" == "200" ]] && echo -e "${GR}${B}$1${R}" || echo -e "${RD}${B}${1:-?}${R}"
}
_df() {   # numeric diff → colored
    local n="${1:-0}"
    if   [[ "$n" -gt 0 ]]; then echo -e "${YL}${B}+$n${R}"
    elif [[ "$n" -lt 0 ]]; then echo -e "${RD}${B}$n${R}"
    else                        echo -e "${DIM}0${R}"
    fi
}

# ── board ─────────────────────────────────────────────────────────────
draw() {
    local containers env_raw be_img we_img git_head data80 data82 releases
    local livez_l livez_p

    containers=$(_containers)
    env_raw=$(_env)
    be_img=$(echo "$env_raw" | grep BACKEND | cut -d= -f2)
    we_img=$(echo "$env_raw" | grep WEB     | cut -d= -f2)
    git_head=$(_git_log)
    data80=$(_data80); data82=$(_data82)
    releases=$(_releases)
    livez_l=$(_livez_local); livez_p=$(_livez_pub)

    local s80 a80 s82 a82 s_diff a_diff
    s80=$(echo "$data80" | sed -n '1p'); a80=$(echo "$data80" | sed -n '2p')
    s82=$(echo "$data82" | sed -n '1p'); a82=$(echo "$data82" | sed -n '2p')
    s_diff=$(( ${s82:-0} - ${s80:-0} )) 2>/dev/null || s_diff=0
    a_diff=$(( ${a82:-0} - ${a80:-0} )) 2>/dev/null || a_diff=0

    local be_st sc_st px_st db_st
    be_st=$(echo "$containers" | awk '/^backend/   {$1=""; print substr($0,2)}')
    sc_st=$(echo "$containers" | awk '/^scheduler/ {$1=""; print substr($0,2)}')
    px_st=$(echo "$containers" | awk '/^proxy/     {$1=""; print substr($0,2)}')
    db_st=$(echo "$containers" | awk '/^db/        {$1=""; print substr($0,2)}')

    local W=54
    echo ""
    echo -e "  ${CY}${B}${TL}$(printf '%*s' $W | tr ' ' "$HZ")${TR}${R}"
    printf "  ${CY}${VT}${R}  ${B}%-${W}s${R}${CY}${VT}${R}\n" "Jose  ·  :80 Deploy Panel"
    echo -e "  ${CY}${BL_}$(printf '%*s' $W | tr ' ' "$HZ")${BR}${R}"

    echo -e "\n  ${B}CONTAINERS${R}"
    printf "    %-12s %s\n" "backend"   "$(_st "${be_st:-}")"
    printf "    %-12s %s\n" "scheduler" "$(_st "${sc_st:-}")"
    printf "    %-12s %s\n" "proxy"     "$(_st "${px_st:-}")"
    printf "    %-12s %s\n" "db"        "$(_st "${db_st:-}")"

    echo -e "\n  ${B}HEALTH${R}    local $(_lz "$livez_l")   public $(_lz "$livez_p")"

    echo -e "\n  ${B}IMAGES${R}    ${DIM}backend  ${be_img:7:12}${R}"
    echo -e "            ${DIM}web      ${we_img:7:12}${R}"

    echo -e "\n  ${B}DATA${R}      ${DIM}:80        :8082      diff${R}"
    printf "    %-10s ${B}%-8s${R}   %-8s   %s\n" "songs"   "${s80:-?}" "${s82:-?}" "$(_df $s_diff)"
    printf "    %-10s ${B}%-8s${R}   %-8s   %s\n" "artists" "${a80:-?}" "${a82:-?}" "$(_df $a_diff)"

    echo -e "\n  ${B}GIT${R}       ${DIM}${git_head:0:54}${R}"

    echo -e "\n  ${B}RELEASES${R}"
    local i=1
    while IFS= read -r rid; do
        [[ -z "$rid" ]] && continue
        local cid; cid=$(_rel_commit "$rid")
        local tag
        [[ "$cid" == "--" ]] && tag="${DIM}--${R}" || tag="${CY}${cid}${R}"
        printf "    ${DIM}%d)${R} %-46s %s\n" "$i" "$rid" "$tag"
        (( i++ ))
    done <<< "$releases"

    # hints
    local hint=""
    if [[ "$s_diff" -gt 0 ]]; then
        echo -e "\n  ${YL}${B}!${R} ${YL}:8082 ahead by ${B}${s_diff}${R}${YL} songs${R} ${DIM}— type${R} ${B}m${R} ${DIM}to merge${R}"
        hint=1
    fi
    [[ -z "$hint" ]] && echo ""

    # command bar
    echo -e "  ${DIM}$(printf '%*s' $W | tr ' ' "$HZ")${R}"
    echo -e "  ${CY}${B}d${R}${DIM})eploy   ${CY}${B}r${R}${DIM})ollback   ${CY}${B}m${R}${DIM})erge   ${CY}${B}l${R}${DIM})ogs   ${CY}${B}q${R}${DIM})uit${R}"
    echo ""
}

# ── actions ───────────────────────────────────────────────────────────
do_deploy() {
    local rid; rid=$(_latest_release)
    [[ -z "$rid" ]] && { err "no valid release found on gandalf"; return; }

    local info
    info=$(_ssh "python3 -c '
import json, sys
try:
    m=json.load(open(\"$RELEASE_ROOT/$rid/release.json\"))
    r=m[\"release\"]
    print(r[\"commit\"][:12], r[\"candidate_branch\"], \"v\"+r[\"backend_version\"], r[\"migration_class\"])
except Exception as e:
    print(\"?\", str(e))
' 2>/dev/null")

    echo ""
    echo -e "  ${B}Release${R}   ${CY}${B}${rid}${R}"
    echo -e "  ${DIM}${info}${R}"
    echo ""
    echo -n "  ${YL}Deploy to :80? [y/N]${R} "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }

    local mp; mp=$(_local_manifest "$rid")
    if [[ ! -f "$mp" ]]; then
        echo -n "  local manifest path: "
        read -r mp
    fi
    [[ -z "$mp" || ! -f "$mp" ]] && { err "manifest not found: $mp"; return; }

    cd "$LOCAL_REPO"
    say "apply …"
    bash "$DEPLOY_TOOL" apply "$mp" --execute --allow-port80-downtime 2>&1 || { err "apply failed"; return; }

    say "verify …"
    bash "$DEPLOY_TOOL" verify "$mp" 2>&1 || { warn "verify returned non-zero — check output above"; return; }

    echo ""
    echo -n "  ${B}Browser evidence JSON path (Enter to skip):${R} "
    read -r evidence
    if [[ -n "$evidence" && -f "$evidence" ]]; then
        say "complete-browser …"
        bash "$DEPLOY_TOOL" complete-browser --execute "$mp" --evidence "$evidence" 2>&1
    else
        warn "browser evidence skipped — run dp80 and supply evidence later if needed"
    fi
    say "deploy complete"
}

do_rollback() {
    local rid; rid=$(_latest_release)
    [[ -z "$rid" ]] && { err "no valid release found"; return; }

    local mp; mp=$(_local_manifest "$rid")
    [[ ! -f "$mp" ]] && { echo -n "  manifest path: "; read -r mp; }
    [[ -z "$mp" || ! -f "$mp" ]] && { err "not found"; return; }

    echo ""
    echo -e "  ${RD}${B}ROLLBACK: code + images only — DB is NOT restored${R}"
    echo -n "  ${RD}Confirm? [y/N]${R} "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }

    cd "$LOCAL_REPO"
    bash "$DEPLOY_TOOL" rollback-code "$mp" --execute --approved-by GeojoLu 2>&1
    say "rollback complete"
}

do_merge() {
    echo ""
    echo -e "  ${B}Merge :8082 business data → :80${R}"
    echo -e "  ${DIM}Migrates songs (and other tables) from 8082 to 80 via dblink${R}"
    echo ""
    echo -n "  ${YL}[g]o  [Enter] cancel:${R} "
    read -r choice
    case "$choice" in
        g)
            echo -n "  ${RD}${B}Write to production DB. Confirm? [y/N]${R} "
            read -r REPLY
            [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
            local script_dir; script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
            local local_script="$script_dir/migrate-songs.sh"
            [[ ! -f "$local_script" ]] && { err "migrate-songs.sh not found at $local_script"; return; }
            say "copying migration script to gandalf …"
            scp -q "$local_script" "${SSH_HOST}:/tmp/migrate-songs.sh"
            say "running migration …"
            ssh -o BatchMode=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=120 "$SSH_HOST" \
                "bash /tmp/migrate-songs.sh" 2>&1
            say "merge complete"
            ;;
        *) say "cancelled" ;;
    esac
}

do_logs() {
    echo -n "  ${DIM}[b]ackend [s]cheduler [p]roxy [d]b:${R} "
    read -r svc
    case "$svc" in
        b) _ssh "docker compose -p datacenter-kimi-production logs --tail=100 backend" ;;
        s) _ssh "docker compose -p datacenter-kimi-production logs --tail=100 scheduler" ;;
        p) _ssh "docker compose -p datacenter-kimi-production logs --tail=100 proxy" ;;
        d) _ssh "docker compose -p datacenter-kimi-production logs --tail=100 db" ;;
        *) warn "unknown service" ;;
    esac
}

pause() { echo ""; echo -n "  ${DIM}[Enter]${R} "; read -r _; }

# ── main loop ─────────────────────────────────────────────────────────
main() {
    while true; do
        draw
        echo -n "  ${B}>${R} "
        read -r choice || { echo ""; exit 0; }
        case "$choice" in
            d)        do_deploy;   pause ;;
            r)        do_rollback; pause ;;
            m)        do_merge;    pause ;;
            l)        do_logs;     pause ;;
            q)        exit 0 ;;
            "")       ;;
            *)        warn "unknown: $choice" ;;
        esac
    done
}

case "${1:-}" in
    status)  draw; exit 0 ;;
    *)
        # Claude Code / CLAUDECODE env: status only (no interactive loop)
        if [ -n "${CLAUDECODE:-}" ]; then draw; exit 0; fi
        main
        ;;
esac
