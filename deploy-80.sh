#!/usr/bin/env bash
# deploy-80.sh — Jose · Gandalf 80 Port Deploy Panel
# repo: github.com/GeojoL/deploy-80
set -euo pipefail

DEPLOY_TOOL="/Users/geojol/Documents/Projects/datacenter-kimi/scripts/release/production-80.sh"
SSH_HOST="gandalf"
PROD_DIR="/home/gandalf/projects/datacenter-kimi-production"
RELEASE_ROOT="/home/gandalf/releases/datacenter-kimi"

# ── mode detect ───────────────────────────────────────────────────────
# Real terminal (user running dp80)   → full color + unicode
# Claude Code (Bash tool / panel)     → plain ASCII, no ANSI garbage
if [ -t 1 ] && [ -z "${CLAUDECODE:-}" ]; then
    C="\033[0m";   B="\033[1m";   d="\033[2m"
    g="\033[32m";  y="\033[33m";  r="\033[31m";  c="\033[36m"
    _ok_mark='●';  _ok_color="$g$B"
    _dn_mark='●';  _dn_color="$r$B"
    _box_tl='┌'; _box_tr='┐'; _box_bl='└'; _box_br='┘'; _box_h='─'; _box_v='│'
    _hint_mark='●'; _dash='—'; _dot='·'
else
    C=""; B=""; d=""; g=""; y=""; r=""; c=""
    _ok_mark='[OK]';  _ok_color=""
    _dn_mark='[DOWN]'; _dn_color=""
    _box_tl='+'; _box_tr='+'; _box_bl='+'; _box_br='+'; _box_h='-'; _box_v='|'
    _hint_mark='*'; _dash='--'; _dot='-'
fi

say()  { echo -e "  ${g}->${C} $*"; }
warn() { echo -e "  ${y}[!]${C} $*"; }
_ssh() { ssh -o BatchMode=yes "$SSH_HOST" "$@" </dev/null 2>/dev/null; }

# ── fetch helpers ─────────────────────────────────────────────────────
_containers() { _ssh "docker compose -p datacenter-kimi-production ps --format '{{.Service}} {{.State}} {{.Status}}' 2>/dev/null"; }
_livez()      { _ssh "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1/api/livez 2>/dev/null"; }
_env()        { _ssh "cat $PROD_DIR/release.env 2>/dev/null"; }
_git_log()    { _ssh "git -C $PROD_DIR log --oneline -1 2>/dev/null"; }
_git_dirty()  { _ssh "git -C $PROD_DIR status --porcelain --untracked-files=no 2>/dev/null | wc -l | tr -d ' '"; }
_data80()     { _ssh "docker exec datacenter-kimi-production-db-1 psql -U datacenter -d datacenter_kimi_production -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null"; }
_data82()     { _ssh "docker exec datacenter-kimi-db-1 psql -U datacenter -d datacenter_kimi_test -tAc 'SELECT count(*) FROM songs; SELECT count(*) FROM artists;' 2>/dev/null || echo -e '?\n?'"; }
_releases()   { _ssh "ls -1t $RELEASE_ROOT/ 2>/dev/null | grep -v '^\.' | grep -v '\.lock' | head -5"; }
_rel_info()   { _ssh "if [ -f $RELEASE_ROOT/$1/release.json ]; then python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$1/release.json\")); print(m.get(\"release\",{}).get(\"commit\",\"?\")[:8])' 2>/dev/null || echo '?'; else echo 'no-manifest'; fi"; }

# ── draw ──────────────────────────────────────────────────────────────
draw() {
    local containers livez be_img we_img git_head git_dirty data80 data82 releases
    containers=$(_containers)
    livez=$(_livez)
    local env_raw; env_raw=$(_env)
    be_img=$(echo "$env_raw" | grep BACKEND | cut -d= -f2)
    we_img=$(echo "$env_raw" | grep WEB | cut -d= -f2)
    git_head=$(_git_log)
    git_dirty=$(_git_dirty)
    data80=$(_data80)
    data82=$(_data82)
    releases=$(_releases)

    local pub_livez
    pub_livez=$(curl -s -o /dev/null -w "%{http_code}" https://gandalf.zebra-diminished.ts.net/api/livez 2>/dev/null)

    local s80 a80 s82 a82
    s80=$(echo "$data80" | head -1); a80=$(echo "$data80" | tail -1)
    s82=$(echo "$data82" | head -1); a82=$(echo "$data82" | tail -1)

    local be_st sc_st px_st db_st
    be_st=$(echo "$containers" | grep ^backend   | cut -d' ' -f2-)
    sc_st=$(echo "$containers" | grep ^scheduler | cut -d' ' -f2-)
    px_st=$(echo "$containers" | grep ^proxy     | cut -d' ' -f2-)
    db_st=$(echo "$containers" | grep ^db        | cut -d' ' -f2-)

    # ── render helpers ────────────────────────────────────────────────
    st() {
        if [[ "$1" == *"healthy"* ]]; then
            echo -e "${_ok_color}${_ok_mark}${C} ${g}$1${C}"
        else
            echo -e "${_dn_color}${_dn_mark}${C} ${r}$1${C}"
        fi
    }
    lz() {
        [[ "$1" == "200" ]] && echo -e "${g}${B}200${C}" || echo -e "${r}${B}$1${C}"
    }
    dr() {
        [[ "$1" != "0" ]] && echo -e "${y}${B}$1 files dirty${C}" || echo -e "${g}clean${C}"
    }
    df() {
        if [[ "$1" -gt 0 ]]; then echo -e "${y}${B}+$1${C}"
        elif [[ "$1" -lt 0 ]]; then echo -e "${r}${B}$1${C}"
        else echo -e "${d}$1${C}"; fi
    }
    _box() {
        local w=52
        echo -e "  ${c}${B}${_box_tl}$(printf "%${w}s" | tr ' ' "${_box_h}")${_box_tr}${C}"
        echo -e "  ${_box_v}              Jose ${_dot} 80 Port Deploy Panel             ${_box_v}"
        echo -e "  ${_box_bl}$(printf "%${w}s" | tr ' ' "${_box_h}")${_box_br}${C}"
    }

    local s_diff=$(( ${s82:-0} - ${s80:-0} )) 2>/dev/null || true
    local a_diff=$(( ${a82:-0} - ${a80:-0} )) 2>/dev/null || true

    # ── board ─────────────────────────────────────────────────────────
    echo ""
    _box

    echo -e "\n  ${B}CONTAINERS${C}"
    printf "    %-12s %s\n" "backend"   "$(st "${be_st:-}")"
    printf "    %-12s %s\n" "scheduler" "$(st "${sc_st:-}")"
    printf "    %-12s %s\n" "proxy"     "$(st "${px_st:-}")"
    printf "    %-12s %s\n" "db"        "$(st "${db_st:-}")"

    echo -e "\n  ${B}HEALTH${C}       local  $(lz "${livez:-}")    public  $(lz "$pub_livez")"

    echo -e "\n  ${B}IMAGES${C}       backend  ${d}${be_img:7:12}${C}"
    echo -e "               web      ${d}${we_img:7:12}${C}"

    echo -e "\n  ${B}DATA${C}         ${d}80         8082       diff${C}"
    printf "    %-12s ${B}%-7s${C}     %-7s     %s\n" "songs"   "${s80:-?}" "${s82:-?}" "$(df "$s_diff")"
    printf "    %-12s ${B}%-7s${C}     %-7s     %s\n" "artists" "${a80:-?}" "${a82:-?}" "$(df "$a_diff")"

    echo -e "\n  ${B}GIT${C}          HEAD  ${d}${git_head:0:52}${C}"
    echo -e "               repo  $(dr "${git_dirty:-1}")"

    echo -e "\n  ${B}RELEASES${C}"
    local i=1
    while IFS= read -r rid; do
        [[ -z "$rid" ]] && continue
        local cid; cid=$(_rel_info "$rid")
        local tag
        if [[ "$cid" == "no-manifest" ]]; then tag="${d}--${C}"; else tag="${c}${cid}${C}"; fi
        printf "    ${d}%d)${C} %-45s %s\n" "$i" "$rid" "$tag"
        ((i++))
    done <<< "$releases"

    # ── hints ─────────────────────────────────────────────────────────
    local any=""
    if [[ "${git_dirty:-0}" != "0" ]]; then
        echo -e "\n  ${y}${B}${_hint_mark}${C} ${y}repo has uncommitted changes${C} ${d}${_dash} type ${B}clean${C}${d} to restore${C}"
        any=1
    fi
    if [[ "$s_diff" -gt 0 ]]; then
        echo -e "  ${y}${B}${_hint_mark}${C} ${y}8082 ahead by ${B}${s_diff}${C} ${y}songs${C} ${d}${_dash} type ${B}promote${C}${d} to sync${C}"
        any=1
    fi
    [[ -n "$any" ]] && echo ""

    # ── commands ──────────────────────────────────────────────────────
    local sep; sep=$(printf '%50s' | tr ' ' "${_box_h}")
    echo -e "  ${c}${B}${sep}${C}"
    echo -e "  ${B}${c}d${C}${d}eploy   ${c}v${C}${d}erify   ${c}r${C}${d}ollback   ${c}clean${C}${d}   ${c}promote${C}${d}   ${c}l${C}${d}ogs   ${c}q${C}${d}uit${C}"
    echo ""
}

# ── actions ───────────────────────────────────────────────────────────
do_deploy() {
    local latest; latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -10 | while read d; do [ -f $RELEASE_ROOT/\$d/release.json ] && echo \$d && break; done")
    [[ -z "$latest" ]] && { warn "no valid release found"; return; }
    local info; info=$(_ssh "python3 -c 'import json; m=json.load(open(\"$RELEASE_ROOT/$latest/release.json\")); r=m[\"release\"]; print(f\"{r[\"commit\"][:12]}  {r[\"candidate_branch\"]}  v{r[\"backend_version\"]}  {r[\"migration_class\"]}\")'")
    echo ""
    echo -e "  release  ${c}${B}${latest}${C}"
    echo -e "           ${d}${info}${C}"
    echo -n "  ${y}deploy? [y/N]${C} "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    local mp="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    [[ -f "$mp" ]] || { echo -n "  local manifest path: "; read -r mp; }
    [[ -z "$mp" || ! -f "$mp" ]] && { warn "not found"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" apply "$mp" --execute --allow-port80-downtime 2>&1
}

do_verify() {
    local latest; latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -1")
    local mp="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    [[ -f "$mp" ]] || { echo -n "  manifest path: "; read -r mp; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" verify "$mp" 2>&1
}

do_rollback() {
    local latest; latest=$(_ssh "ls -1t $RELEASE_ROOT/ | grep -v '^\.' | head -1")
    local mp="/Users/geojol/Documents/Projects/datacenter-kimi/release-manifest/production-80/$latest/release.json"
    [[ -f "$mp" ]] || { echo -n "  manifest path: "; read -r mp; }
    echo -e "  ${r}${B}code/images only ${_dash} DB is NOT restored${C}"
    echo -n "  ${r}confirm? [y/N]${C} "; read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    cd /Users/geojol/Documents/Projects/datacenter-kimi
    python3 "$DEPLOY_TOOL" rollback-code "$mp" --execute --approved-by GeojoLu 2>&1
}

do_clean() {
    local dirty; dirty=$(_ssh "git -C $PROD_DIR status --porcelain 2>/dev/null")
    [[ -z "$dirty" ]] && { say "already clean"; return; }
    echo "$dirty"
    echo -n "  git checkout -- . && git clean -fd? [y/N] "
    read -r REPLY
    [[ "$REPLY" =~ ^[Yy]$ ]] || { say "cancelled"; return; }
    _ssh "cd $PROD_DIR && git checkout -- . && git clean -fd"
    say "done"
}

do_promote() {
    echo -n "  dry-run / go ? [d/g]: "
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
    echo -n "  [b]ackend [s]cheduler [p]roxy [d]b: "
    read -r svc
    case "$svc" in
        b) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 backend" ;;
        s) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 scheduler" ;;
        p) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 proxy" ;;
        d) _ssh "docker compose -p datacenter-kimi-production logs --tail=80 db" ;;
    esac
}

pause() { echo ""; echo -n "  [Enter] "; read -r _; }

# ── loop ──────────────────────────────────────────────────────────────
main() {
    while true; do
        draw
        echo -n "  ${B}>${C} "
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
    *)     if [ -n "${CLAUDECODE:-}" ]; then draw; exit 0; fi  # Claude Code: status only
           main ;;
esac
