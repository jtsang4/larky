package hook

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/router"
	"github.com/jtsang4/larky/internal/state"
)

func TestCodexSessionStartRecoversOnlyItsOwnQueuedReply(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot"}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := request.NewService(store, cfg)
	for _, item := range []struct {
		session string
		message string
		event   string
		text    string
	}{
		{session: "session-a", message: "om-a", event: "evt-a", text: "reply-a"},
		{session: "session-b", message: "om-b", event: "evt-b", text: "reply-b"},
	} {
		req, _, err := service.Create(request.CreateInput{Platform: contract.PlatformCodex, SessionID: item.session, TurnID: item.session, Message: "Done.", AwayDetected: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.RecordDelivery(req.ID, item.message, "oc-chat", "bot", false); err != nil {
			t.Fatal(err)
		}
		if _, err := router.New(store).Handle(contract.IncomingEvent{
			EventID: item.event, Kind: contract.IncomingCardAction, MessageID: item.message, ChatID: "oc-chat", SenderID: "ou-user", Action: "submit_context", Text: item.text,
		}); err != nil {
			t.Fatal(err)
		}
	}

	decision, err := (SessionStartHandler{Requests: service}).Handle(contract.PlatformCodex, strings.NewReader(`{"session_id":"session-a","source":"resume"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.HookSpecificOutput == nil || decision.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(decision.HookSpecificOutput.AdditionalContext, "reply-a") || strings.Contains(decision.HookSpecificOutput.AdditionalContext, "reply-b") {
		t.Fatalf("unexpected SessionStart recovery: %#v", decision)
	}
	if err := store.View(func(db *state.Database) error {
		if len(db.Inbox[state.InboxKey(contract.PlatformCodex, "session-a")]) != 0 {
			t.Fatal("session-a reply was not consumed")
		}
		if len(db.Inbox[state.InboxKey(contract.PlatformCodex, "session-b")]) != 1 {
			t.Fatal("session-b reply was consumed by the wrong task")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
