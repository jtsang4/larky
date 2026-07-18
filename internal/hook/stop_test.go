package hook

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/platform/macos"
	"github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/router"
	"github.com/jtsang4/larky/internal/state"
)

type fixedDetector struct {
	state macos.State
	err   error
}

func (d fixedDetector) Detect() (macos.State, error) { return d.state, d.err }

func TestStopAwayCreatesCardContinuation(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := request.NewService(store, cfg)
	started := false
	handler := StopHandler{
		Config: cfg, Detector: fixedDetector{state: macos.State{Away: true}}, Requests: service,
		Executable: "/tmp/larky binary", EnsureSidecar: func() error { started = true; return nil },
	}
	decision, err := handler.Handle(context.Background(), contract.PlatformClaude, strings.NewReader(`{"session_id":"session-1","turn_id":"turn-1","cwd":"/tmp/project","last_assistant_message":"Done and tested."}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "block" || !strings.Contains(decision.Reason, "larky delivery show --request-id") || !strings.Contains(decision.Reason, "飞书传输") || strings.Contains(decision.Reason, "Done and tested") || len(decision.Reason) > 240 || !started {
		t.Fatalf("unexpected decision: %#v started=%v", decision, started)
	}
}

func TestStopPresentAndRecursiveStopAllow(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := request.NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	handler := StopHandler{Config: cfg, Detector: fixedDetector{state: macos.State{Away: false}}, Requests: service}
	decision, err := handler.Handle(context.Background(), contract.PlatformCodex, strings.NewReader(`{"session_id":"session-1"}`))
	if err != nil || decision.Decision != "" {
		t.Fatalf("present Mac should allow: %#v err=%v", decision, err)
	}
	handler.Detector = fixedDetector{state: macos.State{Away: true}}
	decision, err = handler.Handle(context.Background(), contract.PlatformCodex, strings.NewReader(`{"session_id":"session-1","stop_hook_active":true}`))
	if err != nil || decision.Decision != "" {
		t.Fatalf("recursive Stop should allow: %#v err=%v", decision, err)
	}
}

func TestStopDirectMessageContinuationKeepsTransportDetailsOutOfTheTranscript(t *testing.T) {
	cfg := config.Config{TargetUserID: "ou-user", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := request.NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	handler := StopHandler{
		Config: cfg, Detector: fixedDetector{state: macos.State{Away: true}}, Requests: service,
		Executable: "/tmp/larky", EnsureSidecar: func() error { return nil },
	}
	decision, err := handler.Handle(context.Background(), contract.PlatformClaude, strings.NewReader(`{"session_id":"session-1","last_assistant_message":"Done."}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"ou-user", "<MESSAGE_ID>", "<CHAT_ID_FROM_RESULT>", "Identity contract", "Notification contract", "/tmp/larky"} {
		if strings.Contains(decision.Reason, hidden) {
			t.Fatalf("compact continuation leaked %q:\n%s", hidden, decision.Reason)
		}
	}
}

func TestCodexRecursiveStopReturnsReplyToTheSameHookSession(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot"}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := request.NewService(store, cfg)
	req, _, err := service.Create(request.CreateInput{
		Platform: contract.PlatformCodex, SessionID: "session-a", TurnID: "turn-a", CWD: "/tmp/project", Message: "Tests passed.", AwayDetected: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDelivery(req.ID, "om-a", "oc-chat", "bot", false); err != nil {
		t.Fatal(err)
	}
	_, err = router.New(store).Handle(contract.IncomingEvent{
		EventID: "evt-a", Kind: contract.IncomingCardAction, MessageID: "om-a", ChatID: "oc-chat", SenderID: "ou-user",
		RequestHint: req.ID, Action: "retry", Text: "先修复测试", CallbackToken: "callback-secret", CardContent: `{"schema":"2.0","body":{"elements":[]}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := StopHandler{
		Config: cfg, Detector: fixedDetector{state: macos.State{Away: true}}, Requests: service,
		EnsureSidecar: func() error { return nil }, PollInterval: time.Millisecond,
	}
	decision, err := handler.Handle(context.Background(), contract.PlatformCodex, strings.NewReader(`{"session_id":"session-a","turn_id":"turn-b","stop_hook_active":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "block" || !strings.Contains(decision.Reason, "[Larky · 飞书回复 · "+req.ID+"]") || !strings.Contains(decision.Reason, "先修复测试") || strings.Contains(decision.Reason, "callback-secret") || strings.Contains(decision.Reason, `"schema":"2.0"`) || strings.Contains(decision.Reason, "original-card-content") {
		t.Fatalf("unexpected same-task continuation: %#v", decision)
	}
	stored, err := service.GetForSession(req.ID, contract.PlatformCodex, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != contract.StateResumed || stored.HandoffMode != contract.HandoffCodexStopHook || stored.HandoffEventID != "evt-a" || stored.HandoffAt.IsZero() {
		t.Fatalf("handoff evidence was not persisted: %#v", stored)
	}
	followup, err := handler.Handle(context.Background(), contract.PlatformCodex, strings.NewReader(`{"session_id":"session-a","turn_id":"turn-b","stop_hook_active":true,"last_assistant_message":"Tests passed."}`))
	if err != nil || followup.Decision != "block" || !strings.Contains(followup.Reason, "larky delivery show --request-id") {
		t.Fatalf("remote result should create a follow-up notification: %#v err=%v", followup, err)
	}
	latest, err := service.LatestForSession(contract.PlatformCodex, "session-a", "turn-b")
	if err != nil || latest == nil || latest.ID == req.ID || latest.PreviousRequestID != req.ID {
		t.Fatalf("follow-up request did not preserve the handoff chain: %#v err=%v", latest, err)
	}
}

type sequenceDetector struct {
	mu     sync.Mutex
	states []macos.State
}

func (d *sequenceDetector) Detect() (macos.State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.states) == 0 {
		return macos.State{Away: false}, nil
	}
	state := d.states[0]
	if len(d.states) > 1 {
		d.states = d.states[1:]
	}
	return state, nil
}

func TestCodexWaitStopsWhenTheMacBecomesPresent(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot"}
	service := request.NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	req, _, err := service.Create(request.CreateInput{Platform: contract.PlatformCodex, SessionID: "session-a", TurnID: "turn-a", Message: "Done.", AwayDetected: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDelivery(req.ID, "om-a", "oc-chat", "bot", false); err != nil {
		t.Fatal(err)
	}
	detector := &sequenceDetector{states: []macos.State{{Away: true}, {Away: false}}}
	handler := StopHandler{
		Config: cfg, Detector: detector, Requests: service, EnsureSidecar: func() error { return nil },
		PollInterval: time.Millisecond, AwayInterval: time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	decision, err := handler.Handle(ctx, contract.PlatformCodex, strings.NewReader(`{"session_id":"session-a","turn_id":"turn-a","stop_hook_active":true}`))
	if err != nil || decision.Decision != "" {
		t.Fatalf("unlock should release the Stop hook: %#v err=%v", decision, err)
	}
	stored, err := service.GetForSession(req.ID, contract.PlatformCodex, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != contract.StateCancelled {
		t.Fatalf("unlocked request should be cancelled, got %#v", stored)
	}
}
