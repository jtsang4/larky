package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/sidecar"
	"github.com/jtsang4/larky/internal/state"
)

func TestBuiltBinaryIsAtomicallyReplacedWhileSidecarRuns(t *testing.T) {
	if os.Getenv("LARKY_ATOMIC_REBUILD_TEST") != "1" {
		t.Skip("host rebuild regression test runs only in L3 verification")
	}
	root := projectRoot(t)
	binary := filepath.Join(root, "dist", "larky")
	before := inode(t, binary)
	stateDir, err := os.MkdirTemp("/tmp", "larky-rebuild-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	cfg := config.Config{StateDir: stateDir}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "sidecar", "run", "--no-events")
	cmd.Env = append(os.Environ(), "LARKY_STATE_DIR="+stateDir, "LARKY_EVENT_SOURCE=disabled")
	var sidecarLog bytes.Buffer
	cmd.Stdout = &sidecarLog
	cmd.Stderr = &sidecarLog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForSidecar(t, cfg)

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "make", "build")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("rebuild while sidecar runs: %v\n%s", err, output)
	}
	after := inode(t, binary)
	if after == before {
		t.Fatal("make build overwrote the running executable in place instead of atomically replacing it")
	}
	waitForSidecar(t, cfg)

	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	version := exec.CommandContext(versionCtx, binary, "version")
	if output, err := version.CombinedOutput(); err != nil || len(bytes.TrimSpace(output)) == 0 {
		t.Fatalf("new binary did not start after live rebuild: %v output=%q", err, output)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := sidecar.Stop(stopCtx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("old sidecar did not stop cleanly after live rebuild: %v\n%s", err, sidecarLog.String())
	}
	stopped = true
}

func TestCodexReplyResumesOnlyTheMappedSession(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "larky-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	fakeOutput := filepath.Join(stateDir, "codex-args.txt")
	fakeInput := filepath.Join(stateDir, "codex-stdin.txt")
	fakeCodex := filepath.Join(stateDir, "fake-codex")
	script := "#!/bin/sh\ncat > \"$LARKY_FAKE_CODEX_IN\"\nprintf '%s\\n' \"$@\" > \"$LARKY_FAKE_CODEX_OUT\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKY_FAKE_CODEX_OUT", fakeOutput)
	t.Setenv("LARKY_FAKE_CODEX_IN", fakeInput)
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
	if err := service.RecordDelivery(req.ID, "om-codex-card", "oc-chat", "bot", false); err != nil {
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
			if !strings.Contains(args, "exec\nresume\n11111111-2222-3333-4444-555555555555\n-\n--json") {
				t.Fatalf("wrong Codex resume target: %s", args)
			}
			if strings.Contains(args, "callback-token") || strings.Contains(args, req.ID) {
				t.Fatalf("sensitive routed context leaked into process arguments: %s", args)
			}
			input, inputErr := os.ReadFile(fakeInput)
			if inputErr != nil {
				t.Fatal(inputErr)
			}
			if !strings.Contains(string(input), "request_id="+req.ID) || !strings.Contains(string(input), "callback_token=callback-token") {
				t.Fatalf("wake prompt lost routed context: %s", input)
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

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("file metadata does not expose an inode")
	}
	return stat.Ino
}
