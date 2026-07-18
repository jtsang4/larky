package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/state"
)

func TestUpdateDelegatesToSharedInstaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"))
	}))
	defer server.Close()
	t.Setenv("LARKY_INSTALL_SCRIPT_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"update", "--codex", "--version", "v0.2.0"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"--update", "--codex", "--version", "v0.2.0"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output %q does not contain %q", stdout.String(), expected)
		}
	}
}

func TestUpdateRejectsConflictingSelection(t *testing.T) {
	err := Run(context.Background(), []string{"update", "--all", "--binary-only"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected selection conflict, got %v", err)
	}
}

func TestConfigTargetDefaultsAllowedSender(t *testing.T) {
	t.Setenv("LARKY_STATE_DIR", t.TempDir())
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"config", "set", "--target-user", "ou_target"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"target_user_id": "ou_target"`) ||
		!strings.Contains(stdout.String(), `"allowed_sender_ids": [`) ||
		!strings.Contains(stdout.String(), `"ou_target"`) {
		t.Fatalf("unexpected config output: %s", stdout.String())
	}
}

func TestHandoffShowFetchesOnlyTheExactArchivedReply(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("LARKY_STATE_DIR", stateDir)
	cfg := config.Config{
		StateDir: stateDir, ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"},
		RequestTTL: time.Hour, EventIdentity: "bot",
	}
	store := state.New(filepath.Join(stateDir, "state.json"))
	service := requestsvc.NewService(store, cfg)
	req, _, err := service.Create(requestsvc.CreateInput{Platform: contract.PlatformClaude, SessionID: "claude-session", Message: "Done."})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(db *state.Database) error {
		db.Requests[req.ID].State = contract.StateClaimed
		db.Requests[req.ID].ClaimedEventID = "evt-card"
		db.Inbox[state.InboxKey(contract.PlatformClaude, "claude-session")] = []*state.InboxItem{{Reply: contract.RoutedReply{
			RequestID: req.ID, Platform: contract.PlatformClaude, SessionID: "claude-session", EventID: "evt-card",
			Action: "submit_context", Text: "show me the result", CallbackToken: "callback-secret", CardContent: `{"schema":"2.0"}`,
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TakeReplyForHandoff(req.ID, contract.PlatformClaude, "claude-session", contract.HandoffClaudeMonitor); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"handoff", "show", "--request-id", req.ID, "--platform", "claude", "--session-id", "claude-session"}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var reply contract.RoutedReply
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil || reply.CallbackToken != "callback-secret" || reply.Text != "show me the result" {
		t.Fatalf("unexpected fetched reply: %#v parse=%v raw=%s", reply, err, stdout.String())
	}
	if err := Run(context.Background(), []string{"handoff", "show", "--request-id", req.ID, "--platform", "claude", "--session-id", "another-session"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "exact request") {
		t.Fatalf("wrong session fetched the reply: %v", err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"handoff", "show", "--request-id", req.ID}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil || reply.RequestID != req.ID || reply.Text != "show me the result" {
		t.Fatalf("compact request-id fetch failed: %#v parse=%v raw=%s", reply, err, stdout.String())
	}
}

func TestDeliveryShowPreservesAndChunksTheCompleteTurnOutput(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("LARKY_STATE_DIR", stateDir)
	cfg := config.Config{
		StateDir: stateDir, TargetUserID: "ou-user", AllowedSenderIDs: []string{"ou-user"},
		RequestTTL: time.Hour, EventIdentity: "bot", LarkCLI: "lark-cli",
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	service := requestsvc.NewService(state.New(filepath.Join(stateDir, "state.json")), cfg)
	output := strings.Repeat("完整正文与代码证据。\n", 1200)
	req, _, err := service.Create(requestsvc.CreateInput{Platform: contract.PlatformCodex, SessionID: "session", CWD: "/tmp/project", Message: output})
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"delivery", "show", "--request-id", req.ID}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var plan contract.DeliveryPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("parse delivery plan: %v\n%s", err, stdout.String())
	}
	if plan.TurnOutput != "" || plan.TurnOutputPartCount < 2 || !strings.Contains(plan.PartCommandTemplate, "delivery part") {
		t.Fatalf("long output was inlined instead of exposed as bounded parts: %#v", plan)
	}
	var rebuilt strings.Builder
	for index := 1; index <= plan.TurnOutputPartCount; index++ {
		stdout.Reset()
		if err := Run(context.Background(), []string{"delivery", "part", "--request-id", req.ID, "--index", fmt.Sprint(index)}, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var part contract.DeliveryPart
		if err := json.Unmarshal(stdout.Bytes(), &part); err != nil || part.Index != index || part.Count != plan.TurnOutputPartCount {
			t.Fatalf("unexpected output part: %#v parse=%v", part, err)
		}
		rebuilt.WriteString(part.Content)
	}
	if rebuilt.String() != strings.TrimSpace(output) {
		t.Fatalf("bounded part fetch lost turn output: got=%d want=%d", rebuilt.Len(), len(strings.TrimSpace(output)))
	}
	if plan.CardVersion != "2.0" || !plan.RequireContextForm || plan.RequiredIdentity != "bot" || plan.TargetUserID != "ou-user" || !strings.Contains(plan.RecordCommandTemplate, "--message-id '<MESSAGE_ID_N>'") {
		t.Fatalf("unexpected delivery plan: %#v", plan)
	}
}
