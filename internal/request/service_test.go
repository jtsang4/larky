package request

import (
	"path/filepath"
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
	if err := service.RecordDelivery(first.ID, "om-card", "oc-chat", false); err != nil {
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
	if err := service.RecordDelivery(req.ID, "om-card", "oc-resolved", false); err != nil {
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
