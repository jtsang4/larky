package hook

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/platform/macos"
	"github.com/jtsang4/larky/internal/request"
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
	decision, err := handler.Handle(contract.PlatformClaude, strings.NewReader(`{"session_id":"session-1","turn_id":"turn-1","cwd":"/tmp/project","last_assistant_message":"Done and tested."}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "block" || !strings.Contains(decision.Reason, "Card 2.0") || !strings.Contains(decision.Reason, "delivery record") || !started {
		t.Fatalf("unexpected decision: %#v started=%v", decision, started)
	}
}

func TestStopPresentAndRecursiveStopAllow(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := request.NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	handler := StopHandler{Config: cfg, Detector: fixedDetector{state: macos.State{Away: false}}, Requests: service}
	decision, err := handler.Handle(contract.PlatformCodex, strings.NewReader(`{"session_id":"session-1"}`))
	if err != nil || decision.Decision != "" {
		t.Fatalf("present Mac should allow: %#v err=%v", decision, err)
	}
	handler.Detector = fixedDetector{state: macos.State{Away: true}}
	decision, err = handler.Handle(contract.PlatformCodex, strings.NewReader(`{"session_id":"session-1","stop_hook_active":true}`))
	if err != nil || decision.Decision != "" {
		t.Fatalf("recursive Stop should allow: %#v err=%v", decision, err)
	}
}

func TestStopDirectMessageContinuationRequiresBothDeliveryIDs(t *testing.T) {
	cfg := config.Config{TargetUserID: "ou-user", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := request.NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	handler := StopHandler{
		Config: cfg, Detector: fixedDetector{state: macos.State{Away: true}}, Requests: service,
		Executable: "/tmp/larky", EnsureSidecar: func() error { return nil },
	}
	decision, err := handler.Handle(contract.PlatformClaude, strings.NewReader(`{"session_id":"session-1","last_assistant_message":"Done."}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"direct message to user ou-user", "<MESSAGE_ID>", "<CHAT_ID_FROM_RESULT>", "message_id and chat_id returned by lark-im"} {
		if !strings.Contains(decision.Reason, want) {
			t.Fatalf("continuation prompt missing %q:\n%s", want, decision.Reason)
		}
	}
}
