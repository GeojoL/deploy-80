package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func validPreview() mergePreview {
	return mergePreview{
		SchemaVersion:            1,
		PreviewID:                "preview-20260807T010203Z-Ab12Cd34",
		SourceDigest:             strings.Repeat("a", 64),
		PlanDigest:               strings.Repeat("b", 64),
		SourceSongRows:           30000,
		UniqueSongKeys:           29990,
		DuplicateSourceRows:      10,
		SourceArtistRows:         3000,
		UniqueArtists:            2999,
		FrameworksToInsert:       3,
		LabelsToInsert:           8,
		ArtistsToInsert:          120,
		SongsToInsert:            1278,
		VariantsToBackfill:       47,
		VariantConflicts:         0,
		InvalidReleaseDates:      0,
		PreservedSongDifferences: 21,
		Blockers:                 0,
		ProductionSongsBefore:    50000,
		ProductionSongsAfter:     51278,
		SequenceBumps:            1,
	}
}

func TestParseMergePreviewValidExactCounts(t *testing.T) {
	p := validPreview()
	p.SourceSongRows = 9223372036854775807
	p.UniqueSongKeys = 27499
	p.SongsToInsert = 0
	p.ProductionSongsBefore = 0
	p.ProductionSongsAfter = 0
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	payload := `DP80_EVENT_JSON={"schema_version":1,"phase":"plan","status":"completed","message":"ok"}` +
		"\n" + previewPrefix + string(raw)
	parsed, err := parseMergePreview([]byte(payload))
	if err != nil {
		t.Fatalf("parseMergePreview: %v", err)
	}
	if parsed.SourceSongRows != int64(9223372036854775807) || parsed.UniqueSongKeys != 27499 {
		t.Fatalf("exact counts changed: %+v", parsed)
	}
}

func TestParseMergePreviewRejectsMalformed(t *testing.T) {
	raw, err := json.Marshal(validPreview())
	if err != nil {
		t.Fatal(err)
	}
	base := previewPrefix + string(raw)
	tests := map[string]string{
		"missing terminal":   "ordinary log output",
		"duplicate terminal": base + "\n" + base,
		"negative":           strings.Replace(base, `"songs_to_insert":1278`, `"songs_to_insert":-1`, 1),
		"overflow":           strings.Replace(base, `"source_song_rows":30000`, `"source_song_rows":9223372036854775808`, 1),
		"unknown schema":     strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1),
		"missing field":      strings.Replace(base, `"sequence_cache_blockers":0`, `"not_sequence_cache_blockers":0`, 1),
		"blocker mismatch":   strings.Replace(base, `"blockers":0`, `"blockers":1`, 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMergePreview([]byte(input)); err == nil {
				t.Fatal("expected parse failure")
			}
		})
	}
}

func TestFormatExactNeverAbbreviates(t *testing.T) {
	if got := formatExact(27499); got != "27,499" {
		t.Fatalf("formatExact(27499) = %q", got)
	}
	if got := formatExact(9223372036854775807); got != "9,223,372,036,854,775,807" {
		t.Fatalf("formatExact(max int64) = %q", got)
	}
	if got := formatExactString("0"); got != "0" {
		t.Fatalf("numeric zero became %q", got)
	}
	for _, unknown := range []string{"", "--", "not-collected"} {
		if got := formatExactString(unknown); got != "?" {
			t.Fatalf("unknown %q became %q", unknown, got)
		}
	}
}

func TestDatabasePaneShowsCompleteSeparatedCounts(t *testing.T) {
	m := model{board: boardData{
		songs80: "66316", songs82: "65463",
		artists80: "27499", artists82: "30000",
		pendingMerge: "1278",
	}}
	view := m.renderDatabasePane()
	for _, want := range []string{"66,316", "65,463", "27,499", "30,000", "+2,501", "1,278 songs pending"} {
		if !strings.Contains(view, want) {
			t.Fatalf("database pane omitted exact display %q:\n%s", want, view)
		}
	}
}

func TestRenderMergeConfirmShowsExactPlan(t *testing.T) {
	m := model{termWidth: 98, screen: screenConfirmMerge, mergePreview: validPreview()}
	view := m.View()
	for _, want := range []string{
		"preview-20260807T010203Z-Ab12Cd34", "1,278", "47", "50,000", "51,278", "[M]",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation omitted %q\n%s", want, view)
		}
	}
	m.mergePreviewPage = 1
	view = m.View()
	for _, want := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), "30,000", "29,990"} {
		if !strings.Contains(view, want) {
			t.Fatalf("source details omitted %q\n%s", want, view)
		}
	}
}

func TestRenderMergeConfirmShowsExactBlockerCategory(t *testing.T) {
	p := validPreview()
	p.DuplicateProductionSongs = 796
	p.VariantCycles = 2
	p.SequenceCacheBlockers = 1
	p.Blockers = 799
	m := model{termWidth: 98, screen: screenConfirmMerge, mergePreview: p}
	view := m.View()
	for _, want := range []string{"duplicate prod songs", "796", "variant cycles", "2", "sequence config blockers", "1", "REFUSED"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation omitted blocker detail %q\n%s", want, view)
		}
	}
	for page := 0; page < 3; page++ {
		m.mergePreviewPage = page
		if lines := lineCount(m.View()); lines > 24 {
			t.Fatalf("confirmation page %d needs %d lines, want <= 24", page, lines)
		}
	}
}

func TestMergeConfirmationPagesAndApplyOnlyFromSummary(t *testing.T) {
	m := model{screen: screenConfirmMerge, mergePreview: validPreview()}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	state := next.(model)
	if state.mergePreviewPage != 1 {
		t.Fatalf("Tab selected page %d, want 1", state.mergePreviewPage)
	}
	next, cmd := state.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	state = next.(model)
	if state.screen != screenConfirmMerge || cmd != nil {
		t.Fatal("M started apply from a details page")
	}
}

func TestMStartsPreviewAndKillRemainsInSameTool(t *testing.T) {
	m := model{screen: screenBoard}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if got := next.(model).screen; got != screenMergePreviewing {
		t.Fatalf("m routed to %v, want preview screen", got)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got := next.(model).screen; got != screenConfirmKill {
		t.Fatalf("k routed to %v, want task-termination confirmation", got)
	}
}

func TestLowercaseMDoesNotApplyExactPlan(t *testing.T) {
	m := model{screen: screenConfirmMerge, mergePreview: validPreview()}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if got := next.(model).screen; got != screenConfirmMerge || cmd != nil {
		t.Fatalf("lowercase m unexpectedly started apply: screen=%v cmd=%v", got, cmd)
	}
}

func TestCancelledPreviewCannotBeReopenedByLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := model{
		screen:                screenMergePreviewing,
		mergePreviewRequestID: "preview-request-current",
		mergePreviewCancel:    cancel,
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	state := next.(model)
	if state.screen != screenBoard || state.mergePreviewRequestID != "" || state.mergePreviewCancel != nil {
		t.Fatalf("preview cancellation left active state: %+v", state)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("preview context was not cancelled")
	}

	next, cleanup := state.Update(mergePreviewLoaded{
		requestID: "preview-request-current",
		preview:   validPreview(),
	})
	state = next.(model)
	if state.screen != screenBoard || cleanup == nil {
		t.Fatalf("late preview reopened UI or leaked token: screen=%v cleanup=%v", state.screen, cleanup)
	}
}

func TestConfirmCancelRequestsOneTimeTokenDiscard(t *testing.T) {
	m := model{screen: screenConfirmMerge, mergePreview: validPreview()}
	next, cleanup := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	state := next.(model)
	if state.screen != screenBoard || state.mergePreview.PreviewID != "" || cleanup == nil {
		t.Fatalf("confirmation cancellation did not clear and discard token: %+v cleanup=%v", state, cleanup)
	}
}

func TestLetterBuildVersion(t *testing.T) {
	if appVersion != "1.1.0a" {
		t.Fatalf("appVersion = %q, want 1.1.0a", appVersion)
	}
}

func TestParseLedgerPhaseRequiresCommittedEventFields(t *testing.T) {
	phase, err := parseLedgerPhase(`{"at":"2026-08-07T01:02:03Z","phase":"PREFLIGHT","status":"completed","detail":"kept"}`)
	if err != nil || phase.at != "2026-08-07T01:02:03Z" || phase.phase != "PREFLIGHT" || phase.status != "completed" {
		t.Fatalf("parseLedgerPhase = %+v, %v", phase, err)
	}
	for _, bad := range []string{`{`, `{}`, `{"at":"now","phase":"","status":"completed"}`} {
		if _, err := parseLedgerPhase(bad); err == nil {
			t.Fatalf("accepted malformed ledger event %q", bad)
		}
	}
}

func TestDeployProgressAdvancesOnlyFromStreamEvent(t *testing.T) {
	ch := make(chan deployStreamItem, 1)
	m := model{screen: screenDeploying, deployEvents: ch}
	phase := ledgerPhase{at: "2026-08-07T01:02:03Z", phase: "PREFLIGHT", status: "completed"}
	next, wait := m.Update(deployStreamMsg(deployStreamItem{phase: &phase}))
	state := next.(model)
	if len(state.phases) != 1 || state.phases[0] != phase || wait == nil {
		t.Fatalf("stream event did not advance and continue: phases=%+v wait=%v", state.phases, wait)
	}
	next, _ = state.Update(deployTick(time.Now()))
	state = next.(model)
	if len(state.phases) != 1 {
		t.Fatalf("local spinner tick changed business progress: %+v", state.phases)
	}
}

func TestParseMergeStreamLine(t *testing.T) {
	eventLine := `DP80_EVENT_JSON={"schema_version":1,"phase":"transaction","status":"rolled_back","message":"no writes"}`
	item := parseMergeStreamLine(eventLine)
	if item.err != nil || item.event == nil || item.event.Status != "rolled_back" {
		t.Fatalf("event parse = %+v", item)
	}
	item = parseMergeStreamLine("DP80_EXIT_CODE=17")
	if item.err != nil || item.exitCode == nil || *item.exitCode != 17 {
		t.Fatalf("exit parse = %+v", item)
	}
	for _, bad := range []string{"DP80_EXIT_CODE=-1", "DP80_EXIT_CODE=256", "DP80_EXIT_CODE=nope", "DP80_EVENT_JSON={}"} {
		if got := parseMergeStreamLine(bad); got.err == nil {
			t.Fatalf("accepted malformed stream line %q", bad)
		}
	}
}

func TestMergeWaitsForExitAfterTerminalEvent(t *testing.T) {
	ch := make(chan mergeStreamItem, 1)
	m := model{screen: screenMerging, mergeEvents: ch}
	event := mergeEvent{SchemaVersion: 1, Phase: "result", Status: "committed", Message: "done"}
	next, cmd := m.Update(mergeStreamMsg(mergeStreamItem{event: &event}))
	state := next.(model)
	if state.mergeTerminal || cmd == nil {
		t.Fatalf("terminal event must still wait for process exit: terminal=%v cmd=%v", state.mergeTerminal, cmd)
	}
	rc := 0
	next, _ = state.Update(mergeStreamMsg(mergeStreamItem{exitCode: &rc}))
	state = next.(model)
	if !state.mergeTerminal || state.mergeStatus != "committed" {
		t.Fatalf("exit did not finalize merge: %+v", state)
	}
}

func TestRenderMergeProgressUsesApprovedCounts(t *testing.T) {
	rc := 0
	m := model{
		termWidth:    84,
		screen:       screenMerging,
		mergeStart:   time.Now().Add(-41 * time.Second),
		spin:         5,
		mergePreview: validPreview(),
		mergeJob: mergeJob{
			ID: "merge-20260807T010203Z-0011223344556677", PID: "1284755",
		},
		mergePhase:    "result",
		mergeStatus:   "committed",
		mergeTerminal: true,
		mergeExitCode: &rc,
		mergeLog:      "[completed] plan_check: exact approved plan matched",
	}
	view := m.View()
	for _, want := range []string{"committed", "exit=0", "1,278", "50,000 → 51,278"} {
		if !strings.Contains(view, want) {
			t.Fatalf("progress omitted %q\n%s", want, view)
		}
	}
}

func TestRenderMergeLaunchFailureIsVisible(t *testing.T) {
	m := model{
		termWidth:     84,
		screen:        screenMerging,
		mergeStart:    time.Now(),
		mergeStatus:   "failed",
		mergeTerminal: true,
		mergeLog:      "Error: remote launch refused",
	}
	view := m.View()
	for _, want := range []string{"merge job was not started", "remote launch refused", "[Enter] back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("launch failure omitted %q\n%s", want, view)
		}
	}
}

func TestScriptContainsNoCredentialNetworkMutationOrSharedLog(t *testing.T) {
	b, err := os.ReadFile("migrate-songs.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, forbidden := range []string{
		"SRC_PASS=", "dblink(", "docker network create", "docker network connect",
		"docker network disconnect", "/tmp/migrate-songs.log", "/tmp/migrate_songs_inner.sql",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("script contains forbidden legacy construct %q", forbidden)
		}
	}
}

func TestLegacyShellCannotBypassExactMergeFlow(t *testing.T) {
	b, err := os.ReadFile("deploy-80.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	start := strings.Index(script, "do_merge() {")
	if start < 0 {
		t.Fatal("cannot find legacy do_merge function")
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find end of legacy do_merge function")
	}
	body := script[start : start+end]
	if strings.Contains(body, "migrate-songs.sh") || !strings.Contains(body, "disabled in the legacy shell panel") {
		t.Fatalf("legacy shell can bypass the Go preview/confirm/apply flow:\n%s", body)
	}
}

func TestDeployRuntimeHasNoFixedIntervalNetworkPolling(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(b)
	for _, forbidden := range []string{"func pollLedger(", "case ledgerLoaded:", "cat /home/gandalf/releases/datacenter-kimi/"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("deployment runtime still contains fixed-interval ledger polling: %q", forbidden)
		}
	}
	for _, required := range []string{"tail -n +1 -F ", "waitDeployStream", "runner.CombinedOutput()"} {
		if !strings.Contains(source, required) {
			t.Fatalf("deployment runtime omitted event/process-wait mechanism %q", required)
		}
	}
}
