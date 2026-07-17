package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/state"
)

func TestSidecarRoutesSyntheticEventToExactClaudeSubscriber(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "larky-sidecar-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	cfg := config.Config{
		StateDir: stateDir, ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"},
		RequestTTL: time.Hour, LarkCLI: "lark-cli", CodexCLI: "/usr/bin/false", EventIdentity: "bot",
	}
	store := state.New(cfg.DatabasePath())
	service := requestsvc.NewService(store, cfg)
	req, _, err := service.Create(requestsvc.CreateInput{
		Platform: contract.PlatformClaude, SessionID: "claude-session", TurnID: "turn", CWD: "/tmp/project", Message: "Done.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDelivery(req.ID, "om-card", "oc-chat", false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Run(ctx, cfg, Options{DisableEvents: true, Logger: log.New(io.Discard, "", 0)})
	}()
	waitForPing(t, cfg)

	reader, writer := io.Pipe()
	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- Subscribe(ctx, cfg, contract.PlatformClaude, "claude-session", writer)
	}()

	raw := []byte(`{"event_id":"evt-card","operator_id":"ou-user","message_id":"om-card","chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"` + req.ID + `\",\"action\":\"continue\"}"}`)
	publishCtx, publishCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer publishCancel()
	if _, err := Publish(publishCtx, cfg, "card.action.trigger", raw, true); err != nil {
		t.Fatal(err)
	}

	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		if scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()
	select {
	case line := <-lineCh:
		var notification contract.MonitorNotification
		if err := json.Unmarshal([]byte(line), &notification); err != nil {
			t.Fatal(err)
		}
		reply := notification.Reply
		if reply.SessionID != "claude-session" || reply.Action != "continue" || reply.RequestID != req.ID {
			t.Fatalf("unexpected routed reply: %#v", reply)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for routed reply")
	}

	cancel()
	_ = writer.Close()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("server did not shut down")
	}
	select {
	case <-subscribeDone:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not shut down")
	}
	if _, err := filepath.Abs(cfg.SocketPath()); err != nil {
		t.Fatal(err)
	}
}

func waitForPing(t *testing.T, cfg config.Config) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := Ping(ctx, cfg)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sidecar did not become ready")
}
