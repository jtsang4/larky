package eventbridge

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConsumerWaitsForReadyAndStopsByClosingStdin(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-lark-cli")
	content := `#!/bin/sh
event_key="$3"
printf '[event] ready event_key=%s\n' "$event_key" >&2
printf '{"event_id":"evt-live"}\n'
cat >/dev/null
printf '[event] exited — received 1 event(s) (reason: signal)\n' >&2
`
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan string, 1)
	consumer := Consumer{
		CLI: script, Identity: "bot", Logger: log.New(io.Discard, "", 0),
		OnEvent: func(eventKey string, raw []byte) { seen <- eventKey + ":" + string(raw) },
	}
	done := make(chan error, 1)
	go func() { done <- consumer.runOnce(ctx, "card.action.trigger") }()
	select {
	case value := <-seen:
		if !strings.Contains(value, `card.action.trigger:{"event_id":"evt-live"}`) {
			t.Fatalf("unexpected event: %s", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("consumer did not emit an event after the ready marker")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("unexpected shutdown result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("consumer did not stop after stdin was closed")
	}
}
