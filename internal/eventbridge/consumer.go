package eventbridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Consumer struct {
	CLI      string
	Identity string
	Logger   *log.Logger
	OnEvent  func(eventKey string, raw []byte)
	OnState  func(eventKey string, ready bool)
}

func (c Consumer) Run(ctx context.Context, eventKey string) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.runOnce(ctx, eventKey)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.logf("event consumer %s stopped: %v; restarting in %s", eventKey, err, backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c Consumer) runOnce(ctx context.Context, eventKey string) error {
	readyReported := false
	defer func() {
		if readyReported && c.OnState != nil {
			c.OnState(eventKey, false)
		}
	}()
	cli := c.CLI
	if cli == "" {
		cli = "lark-cli"
	}
	identity := c.Identity
	if identity == "" {
		identity = "bot"
	}
	cmd := exec.Command(cli, "event", "consume", eventKey, "--as", identity)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer stdin.Close()

	ready := make(chan struct{})
	var readyOnce sync.Once
	stderrDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			c.logf("lark-cli[%s]: %s", eventKey, line)
			if strings.Contains(line, "[event] ready event_key="+eventKey) {
				readyOnce.Do(func() { close(ready) })
			}
		}
		stderrDone <- scanner.Err()
	}()
	stdoutDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		buffer := make([]byte, 64*1024)
		scanner.Buffer(buffer, 4*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if c.OnEvent != nil {
				c.OnEvent(eventKey, line)
			}
		}
		stdoutDone <- scanner.Err()
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-ready:
		readyReported = true
		if c.OnState != nil {
			c.OnState(eventKey, true)
		}
		c.logf("event consumer ready: %s", eventKey)
	case err := <-waitDone:
		return fmt.Errorf("exit before ready: %w", normalizeExit(err))
	case <-time.After(20 * time.Second):
		_ = stdin.Close()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-waitDone
		return errors.New("ready marker timeout")
	case <-ctx.Done():
		_ = stdin.Close()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-waitDone
		return ctx.Err()
	}

	select {
	case err := <-waitDone:
		select {
		case scanErr := <-stdoutDone:
			if scanErr != nil {
				return scanErr
			}
		default:
		}
		select {
		case scanErr := <-stderrDone:
			if scanErr != nil {
				return scanErr
			}
		default:
		}
		return normalizeExit(err)
	case <-ctx.Done():
		_ = stdin.Close()
		select {
		case err := <-waitDone:
			if err == nil {
				return ctx.Err()
			}
			return normalizeExit(err)
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Signal(syscall.SIGTERM)
			<-waitDone
			return ctx.Err()
		}
	}
}

func (c Consumer) logf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger.Printf(format, args...)
	}
}

func normalizeExit(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return errors.New("consumer exited")
	}
	return err
}
