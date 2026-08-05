package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── styles ────────────────────────────────────────────────────────────────────

// lipgloss Width(N) sets the box width *inside the border but including
// padding* — usable text width is N minus (2 × horizontal padding), and
// the fully rendered block (with border) is N+2 wide. outerContentWidth
// is that N for the single outer frame (Padding(1,2) → 2 each side), so
// usable text width inside the frame is outerContentWidth-4 = innerTextWidth.
const outerContentWidth = 80
const innerTextWidth = outerContentWidth - 4

// paneWidth is the lipgloss Width() of each side-by-side module box
// (RELEASES / DATABASE), Padding(0,1) → usable text width is paneWidth-2.
// Two panes + border(2 each) + 2-space gap must fill innerTextWidth exactly:
// 2*(paneWidth+2) + 2 == innerTextWidth(76) → paneWidth = 35.
const paneWidth = 35

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
	outerStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")).
			Padding(1, 2).
			Width(outerContentWidth)

	releasesPaneStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("6")).
				Padding(0, 1).
				Width(paneWidth)

	databasePaneStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("5")).
				Padding(0, 1).
				Width(paneWidth)

	paneTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// renderFramed wraps any screen's inner content in the single outer frame.
func renderFramed(content string) string {
	return outerStyle.Render(content)
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

// deployRepo is the local datacenter-kimi checkout holding the release
// runner and prepared manifests; apply runs locally and pushes to gandalf.
const deployRepo = "/Users/geojol/Documents/Projects/datacenter-kimi"

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
}

// findLatestManifest picks the newest prepared release under
// release-manifest/production-80/ (IDs start with a UTC timestamp, so
// lexicographic order is chronological).
func findLatestManifest() (releaseManifest, error) {
	var m releaseManifest
	dir := deployRepo + "/release-manifest/production-80"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, fmt.Errorf("read %s: %w", dir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return m, fmt.Errorf("no prepared release manifest in %s", dir)
	}
	sort.Strings(ids)
	id := ids[len(ids)-1]
	m.path = dir + "/" + id + "/release.json"

	data, err := os.ReadFile(m.path)
	if err != nil {
		return m, err
	}
	var j struct {
		ReleaseID string `json:"release_id"`
		Release   struct {
			Commit         string `json:"commit"`
			BackendVersion string `json:"backend_version"`
			WebVersion     string `json:"web_version"`
		} `json:"release"`
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return m, fmt.Errorf("parse %s: %w", m.path, err)
	}
	m.releaseID = j.ReleaseID
	m.commit = j.Release.Commit
	m.beVersion = j.Release.BackendVersion
	m.weVersion = j.Release.WebVersion
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
	screenConfirmMerge
	screenMerging
	screenConfirmDeploy
	screenDeploying
	screenLog
)

type model struct {
	board       boardData
	loading     bool
	screen      screen
	logContent  string // screenMerging/screenDeploying: raw script output
	auditText   string // screenLog: raw psql audit rows
	backendLogs string // screenLog: raw filtered backend logs
	manifest    releaseManifest
	manifestErr string
	err         string
}

type boardLoaded boardData
type mergeStarted string
type mergeError string
type deployDone string
type deployError string

func loadBoard() tea.Msg {
	d := fetchBoard()
	return boardLoaded(d)
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
	var ai, bi int
	fmt.Sscan(a, &ai)
	fmt.Sscan(b, &bi)
	d := bi - ai
	switch {
	case d > 0:
		return yellow.Bold(true).Render(fmt.Sprintf("+%d", d))
	case d < 0:
		return red.Bold(true).Render(fmt.Sprintf("%d", d))
	default:
		return dim.Render("0")
	}
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
	sb.WriteString(row("songs", bold.Render(padNum(d.songs80)), padNum(d.songs82), colorDiff(d.songs80, d.songs82)) + "\n")
	sb.WriteString(row("artists", bold.Render(padNum(d.artists80)), padNum(d.artists82), colorDiff(d.artists80, d.artists82)) + "\n")
	sb.WriteString("\n")

	var s80, s82 int
	fmt.Sscan(d.songs80, &s80)
	fmt.Sscan(d.songs82, &s82)
	switch {
	case s82 > s80:
		sb.WriteString(yellow.Render(fmt.Sprintf("⚠ %d songs new", s82-s80)) + "\n")
		sb.WriteString(dim.Render("press m to merge") + "\n")
	case s82 == s80:
		sb.WriteString(green.Render("✓ synced") + "\n")
	default:
		sb.WriteString(red.Render("✗ 80 ahead") + "\n")
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
	sb.WriteString("\n")

	// Two-pane row：RELEASES | DATABASE，两个模块高度对齐，不一长一短。
	// 两个面板内容都以 "\n" 结尾——必须先剪掉，否则 lipgloss 会把换行后
	// 的空尾段当成真实一行渲染，实际高度比 lineCount() 多算 1 行，且两边
	// 多出来的量还可能不一样，Height() 对齐就失效了。
	left := strings.TrimRight(m.renderReleasesPane(), "\n")
	right := strings.TrimRight(m.renderDatabasePane(), "\n")
	h := max(lineCount(left), lineCount(right))
	leftBox := releasesPaneStyle.Height(h).Render(left)
	rightBox := databasePaneStyle.Height(h).Render(right)
	sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftBox, "  ", rightBox) + "\n\n")

	// Command bar
	sb.WriteString(dim.Render(strings.Repeat("─", innerTextWidth)) + "\n")
	sb.WriteString(
		cmdKey.Render("d") + cmdDim.Render(")eploy  ") +
			cmdKey.Render("r") + cmdDim.Render(")efresh  ") +
			cmdKey.Render("m") + cmdDim.Render(")erge  ") +
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
				m.screen = screenConfirmMerge
				return m, nil
			case "l":
				m.screen = screenLog
				m.auditText = fetchAuditLog()
				m.backendLogs = fetchBackendLogsClean()
				return m, nil
			}
		case screenConfirmMerge:
			switch msg.String() {
			case "y", "Y":
				m.screen = screenMerging
				m.logContent = ""
				return m, runMerge
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
				return m, runDeploy(m.manifest.path)
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

	case mergeStarted:
		m.logContent = string(msg)
		return m, nil

	case mergeError:
		m.logContent = red.Render("Error: ") + string(msg)
		return m, nil

	case deployDone:
		m.logContent = string(msg)
		return m, nil

	case deployError:
		m.logContent = red.Render("Deploy failed: ") + "\n" + string(msg)
		return m, nil
	}

	return m, nil
}

// runDeploy executes the release runner's apply locally (it pushes the
// bundle to gandalf itself). Long-running: stops/starts prod services,
// dumps both DBs — several minutes.
func runDeploy(manifestPath string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("bash", "scripts/release/production-80.sh",
			"apply", manifestPath, "--execute", "--allow-port80-downtime")
		cmd.Dir = deployRepo
		out, err := cmd.CombinedOutput()
		if err != nil {
			return deployError(string(out) + "\n" + err.Error())
		}
		return deployDone(string(out))
	}
}

func runMerge() tea.Msg {
	scriptDir, _ := os.Executable()
	// find migrate-songs.sh next to the binary
	scriptPath := strings.TrimSuffix(scriptDir, "/dp80") + "/migrate-songs.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		// fallback: same dir as binary
		scriptPath = "migrate-songs.sh"
	}

	// scp then run
	exec.Command("scp", "-q", scriptPath, sshHost+":/tmp/migrate-songs.sh").Run()
	out, err := exec.Command("ssh", "-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=120",
		sshHost, "bash /tmp/migrate-songs.sh").CombinedOutput()
	if err != nil {
		return mergeError(string(out) + "\n" + err.Error())
	}
	return mergeStarted(string(out))
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
			strings.TrimSpace(f[2]),
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
func tailFramed(raw string, n int) string {
	var wrapped []string
	for _, l := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		r := []rune(l)
		if len(r) == 0 {
			wrapped = append(wrapped, "")
			continue
		}
		for len(r) > 0 {
			w := len(r)
			if w > innerTextWidth {
				w = innerTextWidth
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

func (m model) View() string {
	switch m.screen {
	case screenBoard:
		if m.loading {
			return renderFramed(dim.Render("loading…"))
		}
		return renderFramed(m.renderBoardContent())

	case screenConfirmMerge:
		content := titleStyle.Render("Merge :8082 business data → :80") + "\n\n" +
			yellow.Render("This will INSERT new songs into production.") + "\n" +
			yellow.Render("Continue?") + "\n\n" +
			cmdKey.Render("[y]") + dim.Render("es") + "\n" +
			cmdKey.Render("[n]") + dim.Render("/Esc cancel")
		return renderFramed(content)

	case screenConfirmDeploy:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render("Deploy release → :80") + "\n\n")
		if m.manifestErr != "" {
			sb.WriteString(red.Render("✗ " + truncateLine(m.manifestErr, innerTextWidth-2)) + "\n\n")
			sb.WriteString(dim.Render("[Esc/n] back"))
			return renderFramed(sb.String())
		}
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "release", bold.Render(m.manifest.releaseID)))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "commit", cyan.Render(truncateLine(m.manifest.commit, 12))))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "backend", m.manifest.beVersion))
		sb.WriteString(fmt.Sprintf("%-10s %s\n", "web", m.manifest.weVersion))
		sb.WriteString("\n" + yellow.Render("Runs production-80.sh apply (brief :80 downtime).") + "\n\n")
		sb.WriteString(cmdKey.Render("[y]") + dim.Render("es") + "\n")
		sb.WriteString(cmdKey.Render("[n]") + dim.Render("/Esc cancel"))
		return renderFramed(sb.String())

	case screenDeploying:
		if m.logContent == "" {
			return renderFramed(titleStyle.Render("Deploying → :80") + "\n\n" +
				dim.Render("running production-80.sh apply — takes several minutes…"))
		}
		return renderFramed(tailFramed(m.logContent, 28) + "\n\n" + dim.Render("[Enter] back"))

	case screenMerging:
		if m.logContent == "" {
			return renderFramed(dim.Render("running migration…"))
		}
		return renderFramed(tailFramed(m.logContent, 28) + "\n\n" + dim.Render("[Enter] back"))

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
			sb.WriteString("  " + dim.Render(truncateLine(line, innerTextWidth-2)) + "\n")
		}

		sb.WriteString("\n" + dim.Render(strings.Repeat("─", innerTextWidth)) + "\n")
		sb.WriteString(dim.Render("[Esc/q] back"))
		return renderFramed(sb.String())
	}
	return ""
}

// ── entry point ───────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		d := fetchBoard()
		m := model{board: d}
		fmt.Println(renderFramed(m.renderBoardContent()))
		return
	}

	p := tea.NewProgram(model{loading: true}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
