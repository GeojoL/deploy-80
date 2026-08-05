package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── styles ────────────────────────────────────────────────────────────────────

var (
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("6")).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")).
			Padding(0, 1).
			MarginLeft(2)

	sectionLabel = lipgloss.NewStyle().Bold(true)
	cmdKey       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cmdDim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	paneWidth = 38

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
	screenLog
)

type model struct {
	board      boardData
	loading    bool
	screen     screen
	logContent string
	err        string
}

type boardLoaded boardData
type mergeStarted string
type mergeError string

func loadBoard() tea.Msg {
	d := fetchBoard()
	return boardLoaded(d)
}

// ── render helpers ────────────────────────────────────────────────────────────

func colorStatus(state string) string {
	switch {
	case strings.Contains(state, "healthy"):
		return green.Bold(true).Render("● ") + green.Render(state)
	case strings.Contains(state, "Up"):
		return green.Render("● " + state)
	case state == "":
		return dim.Render("--")
	default:
		return red.Bold(true).Render("● ") + red.Render(state)
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
	sb.WriteString(dim.Render("live: ") + cyan.Render(d.currentCommit) + "\n\n")

	if len(d.releases) == 0 {
		sb.WriteString(dim.Render("(none found)") + "\n")
	}

	for i, r := range d.releases {
		commit := "--"
		if i < len(d.releaseCommits) {
			commit = d.releaseCommits[i]
		}
		isLive := commit != "--" && d.currentCommit != "" && strings.HasPrefix(d.currentCommit, commit)

		var marker, name, chash string
		if isLive {
			marker = green.Bold(true).Render("●")
			name = green.Render(truncate(r, paneWidth-8))
			chash = green.Render(commit)
		} else {
			marker = dim.Render("○")
			name = dim.Render(truncate(r, paneWidth-8))
			chash = cyan.Render(commit)
		}
		sb.WriteString(fmt.Sprintf("%s %s\n  %s\n", marker, name, chash))
	}
	return sb.String()
}

func (m model) renderDatabasePane() string {
	d := m.board
	var sb strings.Builder
	sb.WriteString(paneTitle.Render("DATABASE") + "\n")
	sb.WriteString(dim.Render(":80 production  vs  :8082 test") + "\n\n")

	sb.WriteString(dim.Render(fmt.Sprintf("%-8s %7s %8s", "", ":80", ":8082")) + "\n")
	sb.WriteString(fmt.Sprintf("%-8s %7s %8s  %s\n", "songs", bold.Render(d.songs80), d.songs82, colorDiff(d.songs80, d.songs82)))
	sb.WriteString(fmt.Sprintf("%-8s %7s %8s  %s\n", "artists", bold.Render(d.artists80), d.artists82, colorDiff(d.artists80, d.artists82)))

	sb.WriteString("\n")
	var s80, s82 int
	fmt.Sscan(d.songs80, &s80)
	fmt.Sscan(d.songs82, &s82)
	if s82 > s80 {
		sb.WriteString(yellow.Bold(true).Render("!") + " " +
			yellow.Render(fmt.Sprintf("8082 ahead by %d songs", s82-s80)) + "\n")
		sb.WriteString(dim.Render("  press ") + cmdKey.Render("m") + dim.Render(" to merge") + "\n")
	} else {
		sb.WriteString(green.Render("✓ in sync") + "\n")
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if n < 1 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func (m model) renderBoard() string {
	var sb strings.Builder
	d := m.board

	sb.WriteString("\n")
	sb.WriteString(headerStyle.Render("Jose  ·  :80 Deploy Panel") + "\n\n")

	// Top strip: containers + health + images
	cmap := map[string]string{}
	for _, c := range d.containers {
		cmap[c.name] = c.state
	}
	var strip []string
	for _, name := range []string{"backend", "scheduler", "proxy", "db"} {
		strip = append(strip, name+" "+colorStatus(cmap[name]))
	}
	sb.WriteString("  " + strings.Join(strip, dim.Render("  │  ")) + "\n")
	sb.WriteString("  " + dim.Render("health") + " local " + colorHTTP(d.livezLocal) +
		dim.Render("  │  ") + "public " + colorHTTP(d.livezPub) +
		dim.Render("  │  ") + dim.Render("img "+d.beImage+" / "+d.weImage) + "\n\n")

	// Two-pane row: releases | database
	left := releasesPaneStyle.Render(m.renderReleasesPane())
	right := databasePaneStyle.Render(m.renderDatabasePane())
	row := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	sb.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(row) + "\n")

	// Command bar
	sb.WriteString("\n  " + dim.Render(strings.Repeat("─", 2*paneWidth+10)) + "\n")
	sb.WriteString("  " +
		cmdKey.Render("d") + cmdDim.Render(")eploy   ") +
		cmdKey.Render("r") + cmdDim.Render(")ollback   ") +
		cmdKey.Render("m") + cmdDim.Render(")erge   ") +
		cmdKey.Render("l") + cmdDim.Render(")ogs   ") +
		cmdKey.Render("q") + cmdDim.Render(")uit") +
		"\n\n")

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
			case "m":
				m.screen = screenConfirmMerge
				return m, nil
			case "l":
				m.screen = screenLog
				audit := fetchAuditLog()
				logs := fetchBackendLogsClean()
				m.logContent = fmt.Sprintf("=== 迁移审计历史 ===\n%s\n\n=== 后端日志（已过滤噪音）===\n%s", audit, logs)
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
		case screenMerging:
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
	}

	return m, nil
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

func (m model) View() string {
	switch m.screen {
	case screenBoard:
		if m.loading {
			return "\n  " + dim.Render("loading…") + "\n"
		}
		return m.renderBoard()

	case screenConfirmMerge:
		return "\n" +
			"  " + bold.Render("Merge :8082 business data → :80") + "\n\n" +
			"  " + yellow.Render("This will INSERT new songs into production. Continue?") + "\n\n" +
			"  " + cmdKey.Render("[y]") + dim.Render("es   ") +
			cmdKey.Render("[n]") + dim.Render("/Esc cancel") + "\n\n"

	case screenMerging:
		if m.logContent == "" {
			return "\n  " + dim.Render("running migration…") + "\n"
		}
		return "\n" + m.logContent + "\n\n  " + dim.Render("[Enter] back") + "\n"

	case screenLog:
		var sb strings.Builder
		sb.WriteString("\n")

		// 分离审计和日志部分
		parts := strings.Split(m.logContent, "=== 后端日志")

		// 审计部分（带框）
		if len(parts) > 0 {
			auditPart := strings.TrimPrefix(parts[0], "=== 迁移审计历史 ===\n")
			auditLines := strings.Split(strings.TrimSpace(auditPart), "\n")
			auditBox := lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("2")).
				Padding(0, 1).
				Render(strings.Join(auditLines, "\n"))
			sb.WriteString(green.Render("📋 迁移历史\n"))
			sb.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(auditBox))
			sb.WriteString("\n\n")
		}

		// 日志部分（带框）
		if len(parts) > 1 {
			logPart := strings.TrimPrefix(parts[1], " ===\n")
			logLines := strings.Split(strings.TrimSpace(logPart), "\n")
			// 截断过长的行
			var truncatedLines []string
			for _, line := range logLines {
				if len(line) > 100 {
					truncatedLines = append(truncatedLines, line[:97]+"...")
				} else {
					truncatedLines = append(truncatedLines, line)
				}
			}
			logBox := lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("6")).
				Padding(0, 1).
				Render(strings.Join(truncatedLines, "\n"))
			sb.WriteString(cyan.Render("🔍 日志（已过滤 livez 噪音）\n"))
			sb.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(logBox))
			sb.WriteString("\n")
		}

		sb.WriteString("\n  " + dim.Render("[Esc/q] back") + "\n")
		return sb.String()
	}
	return ""
}

// ── entry point ───────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		d := fetchBoard()
		m := model{board: d}
		fmt.Println(m.renderBoard())
		return
	}

	p := tea.NewProgram(model{loading: true}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
