package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

func TestRoutesCardByDeliveryAndClaimsOnce(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	seedRequest(t, store, now)
	r := NewWithClock(store, func() time.Time { return now })
	event := contract.IncomingEvent{
		EventID: "evt-1", Kind: contract.IncomingCardAction, MessageID: "om-1",
		ChatID: "chat-1", SenderID: "user-1", RequestHint: "L7K2AA", Action: "continue",
	}
	result, err := r.Handle(event)
	if err != nil {
		t.Fatalf("route event: %v", err)
	}
	if result.Reply == nil || result.Reply.SessionID != "session-1" || result.Reply.Action != "continue" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := r.Handle(event); err != ErrDuplicate {
		t.Fatalf("expected duplicate, got %v", err)
	}
}

func TestAmbiguousMessageIsNotGuessed(t *testing.T) {
	now := time.Now().UTC()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	for _, id := range []string{"request-a", "request-b"} {
		err := store.Update(func(db *state.Database) error {
			db.Requests[id] = &contract.InteractionRequest{
				ID: id, ShortCode: id, Platform: contract.PlatformClaude, SessionID: id,
				ChatID: "chat-1", AllowedSenderIDs: []string{"user-1"}, State: contract.StatePendingReply,
				CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := NewWithClock(store, func() time.Time { return now })
	_, err := r.Handle(contract.IncomingEvent{EventID: "evt", Kind: contract.IncomingMessage, ChatID: "chat-1", SenderID: "user-1", Text: "continue"})
	if err != ErrUnrouted {
		t.Fatalf("expected unrouted, got %v", err)
	}
}

func TestRoutesReplyToMessage(t *testing.T) {
	now := time.Now().UTC()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	seedRequest(t, store, now)
	r := NewWithClock(store, func() time.Time { return now })
	result, err := r.Handle(contract.IncomingEvent{
		EventID: "evt-message", Kind: contract.IncomingMessage, ChatID: "chat-1", SenderID: "user-1",
		ReplyTo: "om-1", Text: "请继续并先运行测试",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reply == nil || result.Reply.Action != "submit_context" || result.Reply.Text == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestMissingSenderAllowlistFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Update(func(db *state.Database) error {
		db.Requests["NOALLOW"] = &contract.InteractionRequest{
			ID: "NOALLOW", ShortCode: "NOALLOW", Platform: contract.PlatformClaude, SessionID: "session",
			ChatID: "chat-1", State: contract.StatePendingReply, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}
		db.Deliveries["om-card"] = contract.Delivery{RequestID: "NOALLOW", MessageID: "om-card", ChatID: "chat-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := NewWithClock(store, func() time.Time { return now }).Handle(contract.IncomingEvent{
		EventID: "evt", Kind: contract.IncomingCardAction, MessageID: "om-card", ChatID: "chat-1", SenderID: "user-1", Action: "continue",
	})
	if err == nil || !strings.Contains(err.Error(), "sender is not allowed") {
		t.Fatalf("expected fail-closed sender rejection, got %v", err)
	}
}

func seedRequest(t *testing.T, store *state.Store, now time.Time) {
	t.Helper()
	err := store.Update(func(db *state.Database) error {
		db.Requests["L7K2AA"] = &contract.InteractionRequest{
			ID: "L7K2AA", ShortCode: "L7K2AA", Platform: contract.PlatformClaude, SessionID: "session-1",
			ChatID: "chat-1", AllowedSenderIDs: []string{"user-1"}, State: contract.StatePendingReply,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), MessageID: "om-1",
		}
		db.Deliveries["om-1"] = contract.Delivery{RequestID: "L7K2AA", MessageID: "om-1", ChatID: "chat-1", CreatedAt: now}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
