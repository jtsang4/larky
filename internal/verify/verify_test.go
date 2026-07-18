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

func liveStore(t *testing.T, requestCreatedAt, callbackSeenAt time.Time) *state.Store {
	t.Helper()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	err := store.Update(func(db *state.Database) error {
		db.Requests["REQ123"] = &contract.InteractionRequest{
			ID:             "REQ123",
			Platform:       contract.PlatformClaude,
			MessageID:      "message-1",
			AwayDetected:   true,
			ScreenLocked:   true,
			AwayMethod:     "coregraphics",
			ClaimedEventID: "event-1",
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
			Source:  "lark-live",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("create live store: %v", err)
	}
	return store
}
