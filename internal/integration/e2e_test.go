package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/hook"
	"github.com/jtsang4/larky/internal/platform/macos"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/sidecar"
	"github.com/jtsang4/larky/internal/state"
)

func TestBuiltBinaryIsAtomicallyReplacedWhileSidecarRuns(t *testing.T) {
	if os.Getenv("LARKY_ATOMIC_REBUILD_TEST") != "1" {
		t.Skip("host rebuild regression test runs only in L3 verification")
	}
	root := projectRoot(t)
	binary := filepath.Join(root, "dist", "larky")
	before := inode(t, binary)
	stateDir, err := os.MkdirTemp("/tmp", "larky-rebuild-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	cfg := config.Config{StateDir: stateDir}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "sidecar", "run", "--no-events")
	cmd.Env = append(os.Environ(), "LARKY_STATE_DIR="+stateDir, "LARKY_EVENT_SOURCE=disabled")
	var sidecarLog bytes.Buffer
	cmd.Stdout = &sidecarLog
	cmd.Stderr = &sidecarLog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForSidecar(t, cfg)

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "make", "build")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("rebuild while sidecar runs: %v\n%s", err, output)
	}
	after := inode(t, binary)
	if after == before {
		t.Fatal("make build overwrote the running executable in place instead of atomically replacing it")
	}
	waitForSidecar(t, cfg)

	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	version := exec.CommandContext(versionCtx, binary, "version")
	if output, err := version.CombinedOutput(); err != nil || len(bytes.TrimSpace(output)) == 0 {
		t.Fatalf("new binary did not start after live rebuild: %v output=%q", err, output)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := sidecar.Stop(stopCtx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("old sidecar did not stop cleanly after live rebuild: %v\n%s", err, sidecarLog.String())
	}
	stopped = true
}

func TestCodexReplyReturnsThroughOnlyTheMappedStopHook(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "larky-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	cfg := config.Config{
		StateDir: stateDir, ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"},
		RequestTTL: time.Hour, LarkCLI: "unused", EventIdentity: "bot",
	}
	store := state.New(cfg.DatabasePath())
	service := requestsvc.NewService(store, cfg)
	type sessionFixture struct {
		sessionID string
		turnID    string
		messageID string
		eventID   string
		text      string
		request   *contract.InteractionRequest
	}
	fixtures := []*sessionFixture{
		{sessionID: "11111111-2222-3333-4444-555555555555", turnID: "turn-a", messageID: "om-card-a", eventID: "evt-a", text: "only-a"},
		{sessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", turnID: "turn-b", messageID: "om-card-b", eventID: "evt-b", text: "only-b"},
	}
	for _, fixture := range fixtures {
		fixture.request, _, err = service.Create(requestsvc.CreateInput{
			Platform: contract.PlatformCodex, SessionID: fixture.sessionID, TurnID: fixture.turnID,
			CWD: stateDir, Message: "Tests passed.", AwayDetected: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.RecordDelivery(fixture.request.ID, fixture.messageID, "oc-chat", "bot", false); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sidecar.Run(ctx, cfg, sidecar.Options{DisableEvents: true, Logger: log.New(io.Discard, "", 0)})
	}()
	waitForSidecar(t, cfg)

	type hookResult struct {
		decision contract.HookDecision
		err      error
	}
	handler := hook.StopHandler{
		Config: cfg, Detector: integrationDetector{}, Requests: service, EnsureSidecar: func() error { return nil },
		PollInterval: 5 * time.Millisecond, AwayInterval: time.Hour,
	}
	results := []chan hookResult{make(chan hookResult, 1), make(chan hookResult, 1)}
	for index, fixture := range fixtures {
		go func(index int, fixture *sessionFixture) {
			input := fmt.Sprintf(`{"session_id":%q,"turn_id":%q,"stop_hook_active":true}`, fixture.sessionID, fixture.turnID)
			decision, handleErr := handler.Handle(ctx, contract.PlatformCodex, strings.NewReader(input))
			results[index] <- hookResult{decision: decision, err: handleErr}
		}(index, fixture)
	}

	publish := func(fixture *sessionFixture) {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"event_id":%q,"operator_id":"ou-user","message_id":%q,"chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"%s\",\"action\":\"retry\"}","input_value":%q,"token":"callback-token","card_content":"{\"schema\":\"2.0\"}"}`, fixture.eventID, fixture.messageID, fixture.request.ID, fixture.text))
		requestCtx, requestCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer requestCancel()
		if _, err := sidecar.Publish(requestCtx, cfg, "card.action.trigger", raw, true); err != nil {
			t.Fatal(err)
		}
	}

	publish(fixtures[0])
	select {
	case result := <-results[0]:
		if result.err != nil || result.decision.Decision != "block" || !strings.Contains(result.decision.Reason, fixtures[0].text) || strings.Contains(result.decision.Reason, fixtures[1].text) {
			t.Fatalf("session A received the wrong continuation: %#v err=%v", result.decision, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session A Stop hook did not resume")
	}
	select {
	case result := <-results[1]:
		t.Fatalf("session B was resumed by session A's event: %#v", result)
	case <-time.After(150 * time.Millisecond):
	}

	publish(fixtures[1])
	select {
	case result := <-results[1]:
		if result.err != nil || result.decision.Decision != "block" || !strings.Contains(result.decision.Reason, fixtures[1].text) || strings.Contains(result.decision.Reason, fixtures[0].text) {
			t.Fatalf("session B received the wrong continuation: %#v err=%v", result.decision, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session B Stop hook did not resume")
	}

	for _, fixture := range fixtures {
		stored, err := service.GetForSession(fixture.request.ID, contract.PlatformCodex, fixture.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.State != contract.StateResumed || stored.HandoffMode != contract.HandoffCodexStopHook || stored.HandoffEventID != fixture.eventID {
			t.Fatalf("missing exact Stop-hook handoff evidence: %#v", stored)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sidecar did not stop")
	}
}

func TestBuiltCodexHookRoundTripUsesTheOriginalHookProcess(t *testing.T) {
	if os.Getenv("LARKY_BUILT_HOOK_TEST") != "1" {
		t.Skip("built-binary Hook round trip runs only in L3 verification")
	}
	root := projectRoot(t)
	binary := filepath.Join(root, "dist", "larky")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("built larky binary is required: %v", err)
	}
	stateDir, err := os.MkdirTemp("/tmp", "larky-built-hook-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	fakeBin := filepath.Join(stateDir, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	codexSentinel := filepath.Join(stateDir, "codex-was-started")
	fakeCodex := filepath.Join(fakeBin, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nprintf started > \"$LARKY_CODEX_SENTINEL\"\nexit 91\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"LARKY_STATE_DIR="+stateDir,
		"LARKY_CHAT_ID=oc-chat",
		"LARKY_ALLOWED_USER_IDS=ou-user",
		"LARKY_EVENT_SOURCE=disabled",
		"LARKY_TEST_MODE=1",
		"LARKY_AWAY_OVERRIDE=away",
		"LARKY_CODEX_SENTINEL="+codexSentinel,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "sidecar", "stop")
		command.Env = environment
		_ = command.Run()
	})

	run := func(input string, args ...string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = environment
		command.Stdin = strings.NewReader(input)
		return command.CombinedOutput()
	}

	firstInput := `{"session_id":"built-session","turn_id":"built-turn","cwd":"/tmp/project","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"Built process verification passed."}`
	output, err := run(firstInput, "hook", "stop", "--platform", "codex")
	if err != nil {
		t.Fatalf("first built Hook: %v\n%s", err, output)
	}
	var first contract.HookDecision
	if err := json.Unmarshal(output, &first); err != nil || first.Decision != "block" || !strings.Contains(first.Reason, "larky delivery show --request-id") || strings.Contains(first.Reason, "Built process verification passed") || len(first.Reason) > 240 {
		t.Fatalf("first built Hook output: %#v parse=%v raw=%s", first, err, output)
	}

	cfg := config.Config{StateDir: stateDir}
	var requestID string
	if err := state.New(cfg.DatabasePath()).View(func(db *state.Database) error {
		for id, req := range db.Requests {
			if req.Platform == contract.PlatformCodex && req.SessionID == "built-session" {
				requestID = id
			}
		}
		return nil
	}); err != nil || requestID == "" {
		t.Fatalf("built Hook request was not persisted: id=%q err=%v", requestID, err)
	}
	output, err = run("", "delivery", "show", "--request-id", requestID)
	if err != nil {
		t.Fatalf("show built delivery: %v\n%s", err, output)
	}
	var plan contract.DeliveryPlan
	if err := json.Unmarshal(output, &plan); err != nil || plan.TurnOutput != "Built process verification passed." || plan.TurnOutputPartCount != 1 || !plan.RequireContextForm {
		t.Fatalf("built delivery plan lost the turn output: %#v parse=%v raw=%s", plan, err, output)
	}
	output, err = run("", "delivery", "record", "--request-id", requestID, "--message-id", "om-built", "--chat-id", "oc-chat", "--identity", "bot")
	if err != nil {
		t.Fatalf("record built delivery: %v\n%s", err, output)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	waitCommand := exec.CommandContext(waitCtx, binary, "hook", "stop", "--platform", "codex")
	waitCommand.Env = environment
	waitCommand.Stdin = strings.NewReader(`{"session_id":"built-session","turn_id":"built-turn","cwd":"/tmp/project","hook_event_name":"Stop","stop_hook_active":true,"last_assistant_message":"Delivery recorded."}`)
	var waitOutput bytes.Buffer
	waitCommand.Stdout = &waitOutput
	waitCommand.Stderr = &waitOutput
	if err := waitCommand.Start(); err != nil {
		t.Fatal(err)
	}

	raw := fmt.Sprintf(`{"event_id":"evt-built","operator_id":"ou-user","message_id":"om-built","chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"%s\",\"action\":\"retry\"}","input_value":"fix the built test","token":"callback-token","card_content":"{\"schema\":\"2.0\"}"}`, requestID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err = run(raw, "debug", "ingest", "--event-key", "card.action.trigger")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publish built callback: %v\n%s", err, output)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := waitCommand.Wait(); err != nil {
		t.Fatalf("waiting built Hook: %v\n%s", err, waitOutput.String())
	}
	var resumed contract.HookDecision
	if err := json.Unmarshal(waitOutput.Bytes(), &resumed); err != nil || resumed.Decision != "block" || !strings.Contains(resumed.Reason, "[Larky · 飞书回复 · "+requestID+"]") || !strings.Contains(resumed.Reason, "fix the built test") || strings.Contains(resumed.Reason, "callback-token") || strings.Contains(resumed.Reason, `"schema":"2.0"`) {
		t.Fatalf("built Hook did not return the routed continuation: %#v parse=%v raw=%s", resumed, err, waitOutput.String())
	}
	if _, err := os.Stat(codexSentinel); !os.IsNotExist(err) {
		t.Fatalf("Larky started a second codex process; sentinel stat=%v", err)
	}
	stored, err := requestsvc.NewService(state.New(cfg.DatabasePath()), cfg).GetForSession(requestID, contract.PlatformCodex, "built-session")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != contract.StateResumed || stored.HandoffMode != contract.HandoffCodexStopHook || stored.HandoffEventID != "evt-built" {
		t.Fatalf("built round trip lacks original-Hook evidence: %#v", stored)
	}

	recoveryInput := `{"session_id":"recovery-session","turn_id":"recovery-turn","cwd":"/tmp/project","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"Recovery fixture."}`
	output, err = run(recoveryInput, "hook", "stop", "--platform", "codex")
	if err != nil {
		t.Fatalf("create recovery request: %v\n%s", err, output)
	}
	var recoveryRequestID string
	if err := state.New(cfg.DatabasePath()).View(func(db *state.Database) error {
		for id, req := range db.Requests {
			if req.Platform == contract.PlatformCodex && req.SessionID == "recovery-session" {
				recoveryRequestID = id
			}
		}
		return nil
	}); err != nil || recoveryRequestID == "" {
		t.Fatalf("recovery request was not persisted: id=%q err=%v", recoveryRequestID, err)
	}
	if output, err = run("", "delivery", "record", "--request-id", recoveryRequestID, "--message-id", "om-recovery", "--chat-id", "oc-chat", "--identity", "bot"); err != nil {
		t.Fatalf("record recovery delivery: %v\n%s", err, output)
	}
	recoveryEvent := fmt.Sprintf(`{"event_id":"evt-recovery","operator_id":"ou-user","message_id":"om-recovery","chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"%s\",\"action\":\"continue\"}","input_value":"recover in original task"}`, recoveryRequestID)
	if output, err = run(recoveryEvent, "debug", "ingest", "--event-key", "card.action.trigger"); err != nil {
		t.Fatalf("publish recovery callback: %v\n%s", err, output)
	}
	output, err = run(`{"session_id":"recovery-session","cwd":"/tmp/project","hook_event_name":"SessionStart","source":"resume"}`, "hook", "session-start", "--platform", "codex")
	if err != nil {
		t.Fatalf("run SessionStart recovery: %v\n%s", err, output)
	}
	var recovered contract.SessionStartDecision
	if err := json.Unmarshal(output, &recovered); err != nil || recovered.HookSpecificOutput == nil || recovered.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(recovered.HookSpecificOutput.AdditionalContext, "recover in original task") {
		t.Fatalf("SessionStart did not recover the exact inbox: %#v parse=%v raw=%s", recovered, err, output)
	}
	recoveryStored, err := requestsvc.NewService(state.New(cfg.DatabasePath()), cfg).GetForSession(recoveryRequestID, contract.PlatformCodex, "recovery-session")
	if err != nil {
		t.Fatal(err)
	}
	if recoveryStored.HandoffMode != contract.HandoffCodexSessionStart || recoveryStored.HandoffEventID != "evt-recovery" {
		t.Fatalf("SessionStart recovery evidence is incomplete: %#v", recoveryStored)
	}
}

type integrationDetector struct{}

func (integrationDetector) Detect() (macos.State, error) {
	return macos.State{Away: true, Method: "fixture"}, nil
}

func waitForSidecar(t *testing.T, cfg config.Config) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := sidecar.Ping(ctx, cfg)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sidecar did not become ready")
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("file metadata does not expose an inode")
	}
	return stat.Ino
}
