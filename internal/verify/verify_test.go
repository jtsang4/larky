package verify

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

func TestLiveCheckUsesCallbackTimeForFreshness(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	store := liveStore(t, now.Add(-4*time.Hour), now.Add(-10*time.Minute))

	evidence, err := LiveCheck(store, contract.PlatformClaude, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("LiveCheck() error = %v", err)
	}
	if evidence.RequestID != "REQ123" || evidence.EventID != "event-1" {
		t.Fatalf("LiveCheck() evidence = %#v", evidence)
	}
}

func TestLiveCheckRejectsStaleCallback(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	store := liveStore(t, now.Add(-10*time.Minute), now.Add(-3*time.Hour))

	if _, err := LiveCheck(store, contract.PlatformClaude, now.Add(-2*time.Hour)); err == nil {
		t.Fatal("LiveCheck() unexpectedly accepted stale callback evidence")
	}
}

func TestLiveCheckRejectsCallbackStillQueuedForHandoff(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	store := liveStore(t, now.Add(-10*time.Minute), now.Add(-time.Minute))
	err := store.Update(func(db *state.Database) error {
		key := state.InboxKey(contract.PlatformClaude, "session-1")
		db.Inbox[key] = []*state.InboxItem{{Reply: contract.RoutedReply{EventID: "event-1"}}}
		return nil
	})
	if err != nil {
		t.Fatalf("queue callback: %v", err)
	}

	if _, err := LiveCheck(store, contract.PlatformClaude, now.Add(-2*time.Hour)); err == nil {
		t.Fatal("LiveCheck() unexpectedly accepted callback before exact-session handoff")
	}
}

func TestLiveCheckRejectsCodexSessionStartFallbackAsAutomaticResume(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	store := liveStore(t, now.Add(-10*time.Minute), now.Add(-time.Minute))
	if err := store.Update(func(db *state.Database) error {
		req := db.Requests["REQ123"]
		req.Platform = contract.PlatformCodex
		req.HandoffMode = contract.HandoffCodexSessionStart
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LiveCheck(store, contract.PlatformCodex, now.Add(-2*time.Hour)); err == nil {
		t.Fatal("SessionStart recovery must not masquerade as an automatic Stop-hook resume")
	}
}

func liveStore(t *testing.T, requestCreatedAt, callbackSeenAt time.Time) *state.Store {
	t.Helper()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	err := store.Update(func(db *state.Database) error {
		db.Requests["REQ123"] = &contract.InteractionRequest{
			ID:             "REQ123",
			Platform:       contract.PlatformClaude,
			SessionID:      "session-1",
			MessageID:      "message-1",
			State:          contract.StateResumed,
			AwayDetected:   true,
			ScreenLocked:   true,
			AwayMethod:     "coregraphics",
			ClaimedEventID: "event-1",
			HandoffEventID: "event-1",
			HandoffMode:    contract.HandoffClaudeMonitor,
			HandoffAt:      callbackSeenAt,
			CreatedAt:      requestCreatedAt,
		}
		db.Events["event-1"] = state.ProcessedEvent{
			RequestID: "REQ123",
			SeenAt:    callbackSeenAt,
			Source:    "lark-live",
		}
		db.Verification = append(db.Verification, contract.IncomingEvent{
			EventID: "event-1",
			Kind:    contract.IncomingCardAction,
			Action:  "continue",
			Source:  "lark-live",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("create live store: %v", err)
	}
	return store
}
