package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
)

func Ensure(cfg config.Config, executable string) error {
	requireEvents := os.Getenv("LARKY_EVENT_SOURCE") != "disabled"
	if executable == "" {
		var resolveErr error
		executable, resolveErr = os.Executable()
		if resolveErr != nil {
			return resolveErr
		}
	}
	digest, err := fileDigest(executable)
	if err != nil {
		return fmt.Errorf("digest current executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	status, err := GetStatus(ctx, cfg)
	cancel()
	if err == nil {
		if status.ExecutableDigest == digest {
			return waitUntilReady(cfg, requireEvents, time.Now().Add(8*time.Second))
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		stopErr := Stop(stopCtx, cfg)
		stopCancel()
		if stopErr != nil {
			return fmt.Errorf("stop outdated sidecar: %w", stopErr)
		}
		if err := waitUntilStopped(cfg, time.Now().Add(3*time.Second)); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(cfg.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open sidecar log: %w", err)
	}
	cmd := exec.Command(executable, "sidecar", "run", "--background-child")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start sidecar: %w", err)
	}
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return waitUntilReady(cfg, requireEvents, time.Now().Add(8*time.Second))
}

func waitUntilStopped(cfg config.Config, deadline time.Time) error {
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := Ping(ctx, cfg)
		cancel()
		if err != nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("outdated sidecar did not stop")
}

func waitUntilReady(cfg config.Config, requireEvents bool, deadline time.Time) error {
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		status, err := GetStatus(ctx, cfg)
		cancel()
		if err == nil && (!requireEvents || status.EventsReady) {
			return nil
		}
		if err != nil {
			lastErr = err
		} else if !status.EventsEnabled {
			lastErr = errors.New("sidecar event consumers are disabled")
		} else {
			lastErr = errors.New("sidecar event consumers are not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("sidecar did not become ready: %w (see %s)", lastErr, cfg.LogPath())
}

func Ping(ctx context.Context, cfg config.Config) error {
	var reply response
	if err := request(ctx, cfg, command{Op: "ping"}, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return errors.New(reply.Error)
	}
	return nil
}

func GetStatus(ctx context.Context, cfg config.Config) (Status, error) {
	var reply response
	if err := request(ctx, cfg, command{Op: "status"}, &reply); err != nil {
		return Status{}, err
	}
	if !reply.OK || reply.Status == nil {
		return Status{}, errors.New(reply.Error)
	}
	return *reply.Status, nil
}

func Stop(ctx context.Context, cfg config.Config) error {
	var reply response
	if err := request(ctx, cfg, command{Op: "stop"}, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return errors.New(reply.Error)
	}
	return nil
}

func Publish(ctx context.Context, cfg config.Config, eventKey string, raw []byte, synthetic bool) (response, error) {
	var reply response
	err := request(ctx, cfg, command{Op: "publish", EventKey: eventKey, Event: raw, Synthetic: synthetic}, &reply)
	if err == nil && !reply.OK {
		err = errors.New(reply.Error)
	}
	return reply, err
}

func Subscribe(ctx context.Context, cfg config.Config, platform contract.Platform, sessionID string, output io.Writer) error {
	if !platform.Valid() || sessionID == "" {
		return errors.New("valid platform and session_id are required")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", cfg.SocketPath())
	if err != nil {
		return err
	}
	defer conn.Close()
	closeOnCancel := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closeOnCancel:
		}
	}()
	defer close(closeOnCancel)
	if err := json.NewEncoder(conn).Encode(command{Op: "subscribe", Platform: platform, SessionID: sessionID}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(conn)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		var reply response
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			return fmt.Errorf("decode sidecar response: %w", err)
		}
		if !reply.OK {
			return errors.New(reply.Error)
		}
		if reply.Reply == nil {
			continue
		}
		if _, err := fmt.Fprintln(output, monitorNotification(*reply.Reply)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func monitorNotification(reply contract.RoutedReply) string {
	text := strings.Join(strings.Fields(reply.Text), " ")
	if text == "" {
		text = map[string]string{"continue": "用户选择继续当前任务。", "retry": "用户选择重试当前任务。"}[reply.Action]
	}
	if text == "" {
		text = "用户提交了操作：" + reply.Action
	}
	runes := []rune(text)
	if len(runes) > 500 {
		text = string(runes[:500]) + "…"
	}
	return fmt.Sprintf("[Larky · 飞书回复 · %s] %s", reply.RequestID, text)
}

func request(ctx context.Context, cfg config.Config, cmd command, reply *response) error {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", cfg.SocketPath())
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return err
	}
	if err := json.NewDecoder(conn).Decode(reply); err != nil {
		return err
	}
	return nil
}
