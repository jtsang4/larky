package request

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

func TestCreateIsIdempotentAndDeliveryBuildsMapping(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := NewServiceWithClock(store, cfg, func() time.Time { return now })
	input := CreateInput{Platform: contract.PlatformCodex, SessionID: "session", TurnID: "turn", CWD: "/tmp/project", Message: "Done and tested."}
	first, created, err := service.Create(input)
	if err != nil || !created {
		t.Fatalf("create request: created=%v err=%v", created, err)
	}
	second, created, err := service.Create(input)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("expected idempotent request: %#v created=%v err=%v", second, created, err)
	}
	if err := service.RecordDelivery(first.ID, "om-card", "oc-chat", "bot", false); err != nil {
		t.Fatal(err)
	}
	if err := store.View(func(db *state.Database) error {
		if db.Requests[first.ID].State != contract.StatePendingReply || db.Deliveries["om-card"].RequestID != first.ID {
			t.Fatalf("delivery mapping was not persisted: %#v", db)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRequiresExplicitSecurityScope(t *testing.T) {
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := NewService(store, config.Config{ChatID: "oc-chat", RequestTTL: time.Hour})
	_, _, err := service.Create(CreateInput{Platform: contract.PlatformClaude, SessionID: "session"})
	if err == nil {
		t.Fatal("expected missing sender configuration error")
	}
}

func TestDirectMessageTargetLearnsChatFromDelivery(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	cfg := config.Config{TargetUserID: "ou-target", AllowedSenderIDs: []string{"ou-target"}, RequestTTL: time.Hour}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := NewServiceWithClock(store, cfg, func() time.Time { return now })
	req, created, err := service.Create(CreateInput{Platform: contract.PlatformClaude, SessionID: "session", Message: "Done."})
	if err != nil || !created {
		t.Fatalf("create direct-message request: created=%v err=%v", created, err)
	}
	if req.ChatID != "" || req.TargetUserID != "ou-target" {
		t.Fatalf("unexpected target before delivery: %#v", req)
	}
	if err := service.RecordDelivery(req.ID, "om-card", "oc-resolved", "bot", false); err != nil {
		t.Fatal(err)
	}
	if err := store.View(func(db *state.Database) error {
		stored := db.Requests[req.ID]
		if stored.ChatID != "oc-resolved" || db.Deliveries["om-card"].ChatID != "oc-resolved" {
			t.Fatalf("resolved direct-message chat was not persisted: %#v", db)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordDeliveryRejectsIdentityThatCannotReachConsumer(t *testing.T) {
	cfg := config.Config{
		ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot",
	}
	service := NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	req, _, err := service.Create(CreateInput{Platform: contract.PlatformClaude, SessionID: "session", Message: "Done."})
	if err != nil {
		t.Fatal(err)
	}
	err = service.RecordDelivery(req.ID, "om-card", "oc-chat", "user", false)
	if err == nil || !strings.Contains(err.Error(), `delivery identity "user" does not match event consumer identity "bot"`) {
		t.Fatalf("expected identity mismatch error, got %v", err)
	}
}

func TestReplyHandoffRequiresTheExactRequestSessionAndEvent(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot"}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := NewService(store, cfg)
	req, _, err := service.Create(CreateInput{Platform: contract.PlatformCodex, SessionID: "session-a", TurnID: "turn-a", Message: "Done."})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDelivery(req.ID, "om-a", "oc-chat", "bot", false); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(db *state.Database) error {
		db.Requests[req.ID].State = contract.StateClaimed
		db.Requests[req.ID].ClaimedEventID = "evt-a"
		db.Inbox[state.InboxKey(contract.PlatformCodex, "session-a")] = []*state.InboxItem{{Reply: contract.RoutedReply{
			RequestID: req.ID, Platform: contract.PlatformCodex, SessionID: "session-a", EventID: "evt-a", Action: "continue",
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	wrong, err := service.TakeReplyForHandoff(req.ID, contract.PlatformCodex, "session-b", contract.HandoffCodexStopHook)
	if err != nil || wrong != nil {
		t.Fatalf("another session must not claim the reply: %#v err=%v", wrong, err)
	}
	reply, err := service.TakeReplyForHandoff(req.ID, contract.PlatformCodex, "session-a", contract.HandoffCodexStopHook)
	if err != nil || reply == nil || reply.EventID != "evt-a" {
		t.Fatalf("exact session did not claim the reply: %#v err=%v", reply, err)
	}
	stored, err := service.GetForSession(req.ID, contract.PlatformCodex, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != contract.StateResumed || stored.HandoffMode != contract.HandoffCodexStopHook || stored.HandoffEventID != "evt-a" || stored.HandoffAt.IsZero() {
		t.Fatalf("handoff evidence was not recorded: %#v", stored)
	}
}

func TestLatestForSessionDoesNotCrossTurns(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := NewServiceWithClock(state.New(filepath.Join(t.TempDir(), "state.json")), cfg, func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	first, _, err := service.Create(CreateInput{Platform: contract.PlatformCodex, SessionID: "session", TurnID: "turn-a", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Create(CreateInput{Platform: contract.PlatformCodex, SessionID: "session", TurnID: "turn-b", Message: "second"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := service.LatestForSession(contract.PlatformCodex, "session", "turn-a")
	if err != nil || latest == nil || latest.ID != first.ID || latest.ID == second.ID {
		t.Fatalf("turn lookup crossed sessions: %#v err=%v", latest, err)
	}
}
