package integration

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/sidecar"
	"github.com/jtsang4/larky/internal/state"
)

func TestCodexReplyResumesOnlyTheMappedSession(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "larky-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	fakeOutput := filepath.Join(stateDir, "codex-args.txt")
	fakeCodex := filepath.Join(stateDir, "fake-codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$LARKY_FAKE_CODEX_OUT\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKY_FAKE_CODEX_OUT", fakeOutput)
	cfg := config.Config{
		StateDir: stateDir, ChatID: "oc-chat", AllowedSenderIDs: []string{"ou-user"},
		RequestTTL: time.Hour, LarkCLI: "unused", CodexCLI: fakeCodex, EventIdentity: "bot",
	}
	store := state.New(cfg.DatabasePath())
	service := requestsvc.NewService(store, cfg)
	req, _, err := service.Create(requestsvc.CreateInput{
		Platform: contract.PlatformCodex, SessionID: "11111111-2222-3333-4444-555555555555",
		TurnID: "turn", CWD: stateDir, Message: "Tests passed.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordDelivery(req.ID, "om-codex-card", "oc-chat", false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sidecar.Run(ctx, cfg, sidecar.Options{DisableEvents: true, Logger: log.New(io.Discard, "", 0)})
	}()
	waitForSidecar(t, cfg)

	raw := []byte(fmt.Sprintf(`{"event_id":"evt-codex","operator_id":"ou-user","message_id":"om-codex-card","chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"%s\",\"action\":\"retry\"}","token":"callback-token","card_content":"{\"schema\":\"2.0\"}"}`, req.ID))
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer requestCancel()
	if _, err := sidecar.Publish(requestCtx, cfg, "card.action.trigger", raw, true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(fakeOutput)
		if readErr == nil {
			args := string(data)
			if !strings.Contains(args, "exec\nresume\n11111111-2222-3333-4444-555555555555") {
				t.Fatalf("wrong Codex resume target: %s", args)
			}
			if !strings.Contains(args, "request_id="+req.ID) || !strings.Contains(args, "callback_token=callback-token") {
				t.Fatalf("wake prompt lost routed context: %s", args)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sidecar did not stop")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("fake Codex runner was not invoked")
}

func waitForSidecar(t *testing.T, cfg config.Config) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := sidecar.Ping(ctx, cfg)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sidecar did not become ready")
}
