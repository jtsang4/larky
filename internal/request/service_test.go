package request

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	if err := service.RecordDelivery(first.ID, "om-card", "oc-chat", "bot", false); err != nil {
		t.Fatalf("identical delivery receipt should be idempotent: %v", err)
	}
	if err := service.RecordDeliveries(first.ID, []string{"om-card", "om-content", "om-content"}, "oc-chat", "bot", false); err != nil {
		t.Fatalf("content aliases should extend an awaiting delivery: %v", err)
	}
	if err := store.View(func(db *state.Database) error {
		stored := db.Requests[first.ID]
		if stored.State != contract.StatePendingReply || stored.MessageID != "om-card" || len(stored.MessageIDs) != 2 || db.Deliveries["om-card"].RequestID != first.ID || db.Deliveries["om-content"].RequestID != first.ID {
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
			CallbackToken: "callback-secret", CardContent: `{"schema":"2.0","body":{"elements":[]}}`,
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
	if wrong, err := service.GetHandoffReply(req.ID, contract.PlatformCodex, "session-b"); err != nil || wrong != nil {
		t.Fatalf("another session must not fetch the archived reply: %#v err=%v", wrong, err)
	}
	if wrong, err := service.GetHandoffReply(req.ID, contract.PlatformClaude, "session-a"); err != nil || wrong != nil {
		t.Fatalf("another platform must not fetch the archived reply: %#v err=%v", wrong, err)
	}
	archived, err := service.GetHandoffReply(req.ID, contract.PlatformCodex, "session-a")
	if err != nil || archived == nil || archived.CallbackToken != "callback-secret" || !strings.Contains(archived.CardContent, `"schema":"2.0"`) {
		t.Fatalf("complete exact-session handoff was not archived: %#v err=%v", archived, err)
	}
	if err := service.RecordDelivery(req.ID, "om-content", "oc-chat", "bot", false); err == nil || !strings.Contains(err.Error(), "state resumed") {
		t.Fatalf("a result message reopened a resumed request: %v", err)
	}
	stored, err = service.GetForSession(req.ID, contract.PlatformCodex, "session-a")
	if err != nil || stored.State != contract.StateResumed || stored.MessageID != "om-a" {
		t.Fatalf("rejected result delivery changed the resumed request: %#v err=%v", stored, err)
	}
}

func TestArchivedHandoffRecoversCard20ContextValue(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour, EventIdentity: "bot"}
	store := state.New(filepath.Join(t.TempDir(), "state.json"))
	service := NewService(store, cfg)
	req, _, err := service.Create(CreateInput{Platform: contract.PlatformClaude, SessionID: "session", Message: "Done."})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"type":"card.action.trigger","event_id":"evt-card20","operator_id":"ou-user","message_id":"om-card","chat_id":"oc-chat","action_name":"submit_context","form_value":"{\"context_value\":\"好的，这是我的回复。\"}"}`)
	if err := store.Update(func(db *state.Database) error {
		db.Requests[req.ID].State = contract.StateResumed
		db.Requests[req.ID].ClaimedEventID = "evt-card20"
		db.Requests[req.ID].HandoffEventID = "evt-card20"
		db.Handoffs[req.ID] = contract.RoutedReply{
			RequestID: req.ID, Platform: contract.PlatformClaude, SessionID: "session",
			EventID: "evt-card20", Action: "submit_context",
		}
		db.Verification = append(db.Verification, contract.IncomingEvent{
			EventID: "evt-card20", Kind: contract.IncomingCardAction, ReceivedAt: time.Now(), Raw: raw,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := service.GetHandoffReplyByID(req.ID)
	if err != nil || reply == nil || reply.Text != "好的，这是我的回复。" {
		t.Fatalf("archived Card 2.0 input was not recovered: %#v err=%v", reply, err)
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

func TestClassifyPrefersAnExplicitLeadingOutcome(t *testing.T) {
	tests := []struct {
		message string
		want    contract.RequestStatus
	}{
		{"Done, as instructed.\nThe first send failed, then I fixed it and resent successfully.", contract.StatusDone},
		{"已完成：第一次发送失败，修复后重发成功。", contract.StatusDone},
		{"Failed: the live callback still errors.", contract.StatusFailed},
		{"Blocked: waiting for a local permission decision.", contract.StatusBlocked},
		{"Please choose which option to use?", contract.StatusWaitingUser},
	}
	for _, test := range tests {
		if got := Classify(test.message); got != test.want {
			t.Errorf("Classify(%q) = %s, want %s", test.message, got, test.want)
		}
	}
}

func TestCreateKeepsTheCompleteTurnOutputSeparateFromItsSummary(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	output := "已完成：\n" + strings.Repeat("这是完整结果、验证证据和代码内容。", 500)
	req, _, err := service.Create(CreateInput{Platform: contract.PlatformCodex, SessionID: "session", TurnID: "turn", Message: output})
	if err != nil {
		t.Fatal(err)
	}
	if req.TurnOutput != output || utf8.RuneCountInString(req.Summary) > 1201 || !strings.HasSuffix(req.Summary, "…") || req.TurnOutputTruncated {
		t.Fatalf("complete turn output was not preserved separately: summary=%d output=%d cut=%v", utf8.RuneCountInString(req.Summary), utf8.RuneCountInString(req.TurnOutput), req.TurnOutputTruncated)
	}
}

func TestCreateBoundsVeryLargeTurnOutputAtAValidUTF8Boundary(t *testing.T) {
	cfg := config.Config{ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"}, RequestTTL: time.Hour}
	service := NewService(state.New(filepath.Join(t.TempDir(), "state.json")), cfg)
	output := strings.Repeat("界", maxTurnOutputBytes)
	req, _, err := service.Create(CreateInput{Platform: contract.PlatformCodex, SessionID: "session", TurnID: "turn", Message: output})
	if err != nil {
		t.Fatal(err)
	}
	if !req.TurnOutputTruncated || !utf8.ValidString(req.TurnOutput) || !strings.Contains(req.TurnOutput, "exceeded 128 KiB") || len(req.TurnOutput) > maxTurnOutputBytes+256 {
		t.Fatalf("large turn output was not safely bounded: bytes=%d valid=%v cut=%v", len(req.TurnOutput), utf8.ValidString(req.TurnOutput), req.TurnOutputTruncated)
	}
}
