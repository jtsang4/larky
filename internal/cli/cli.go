package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/jtsang4/larky/internal/codexhooks"
	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/hook"
	"github.com/jtsang4/larky/internal/platform/macos"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/sidecar"
	"github.com/jtsang4/larky/internal/state"
	"github.com/jtsang4/larky/internal/updater"
	"github.com/jtsang4/larky/internal/verify"
)

var Version = "dev"

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	case "version", "--version":
		fmt.Fprintln(stdout, Version)
		return nil
	case "away":
		return runAway(stdout)
	case "config":
		return runConfig(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, stdout)
	case "update":
		return runUpdate(ctx, args[1:], stdout, stderr)
	case "hook":
		return runHook(ctx, args[1:], stdin, stdout)
	case "delivery":
		return runDelivery(args[1:], stdout, stderr)
	case "handoff":
		return runHandoff(ctx, args[1:], stdout, stderr)
	case "sidecar":
		return runSidecar(ctx, args[1:], stdout, stderr)
	case "subscribe":
		return runSubscribe(ctx, args[1:], stdout, stderr)
	case "debug":
		return runDebug(ctx, args[1:], stdin, stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run larky help", args[0])
	}
}

func usage(output io.Writer) {
	fmt.Fprint(output, `larky — route Lark interactions back to paused coding-agent sessions

Usage:
  larky away
  larky config set [--chat-id <oc_...> | --target-user <ou_...>] [--allowed-user <ou_...>]
  larky config show
  larky doctor
  larky update [--version <vX.Y.Z>] [--claude|--codex|--all|--binary-only]
  larky hook stop --platform claude|codex
  larky hook session-start --platform codex
  larky delivery record --request-id <ID> --message-id <om_...> --chat-id <oc_...> --identity bot|user
  larky delivery fail --request-id <ID>
  larky handoff show --request-id <ID> --platform claude|codex --session-id <ID>
  larky sidecar run|status|stop
  larky subscribe --platform claude --session-id <UUID>
  larky debug ingest --event-key <key> < event.json
  larky verify plan|run|status
`)
}

func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.String("version", "", "release version, for example v0.2.0 (defaults to latest)")
	claude := flags.Bool("claude", false, "install or update the Claude Code plugin")
	codex := flags.Bool("codex", false, "install or update the Codex plugin")
	all := flags.Bool("all", false, "install or update both plugins")
	binaryOnly := flags.Bool("binary-only", false, "update only the larky command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("update does not accept positional arguments")
	}
	if *binaryOnly && (*claude || *codex || *all) {
		return errors.New("--binary-only cannot be combined with plugin selection flags")
	}

	installerArgs := make([]string, 0, 6)
	if *version != "" {
		installerArgs = append(installerArgs, "--version", *version)
	}
	if *binaryOnly {
		installerArgs = append(installerArgs, "--binary-only")
	} else if *all {
		installerArgs = append(installerArgs, "--all")
	} else {
		if *claude {
			installerArgs = append(installerArgs, "--claude")
		}
		if *codex {
			installerArgs = append(installerArgs, "--codex")
		}
	}

	scriptURL := strings.TrimSpace(os.Getenv("LARKY_INSTALL_SCRIPT_URL"))
	return (updater.Runner{ScriptURL: scriptURL}).Run(ctx, Version, installerArgs, stdout, stderr)
}

func runAway(output io.Writer) error {
	value, err := (macos.SystemDetector{}).Detect()
	if err != nil {
		return err
	}
	return writeJSON(output, value)
}

func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("config requires set or show")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "show":
		cfg, err = config.ResolveRuntime(ctx, cfg)
		if err != nil {
			return err
		}
		return writeJSON(stdout, cfg)
	case "set":
		flags := flag.NewFlagSet("config set", flag.ContinueOnError)
		flags.SetOutput(stderr)
		chatID := flags.String("chat-id", "", "target Lark chat_id")
		targetUserID := flags.String("target-user", "", "target Lark user open_id for direct messages")
		var users stringListFlag
		flags.Var(&users, "allowed-user", "allowed Lark open_id (repeatable)")
		ttl := flags.Duration("request-ttl", 0, "request lifetime, for example 24h")
		larkCLI := flags.String("lark-cli", "", "lark-cli executable path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *chatID != "" {
			cfg.ChatID = *chatID
		}
		if *targetUserID != "" {
			cfg.TargetUserID = *targetUserID
			if len(users) == 0 {
				cfg.AllowedSenderIDs = []string{*targetUserID}
			}
		}
		if len(users) > 0 {
			cfg.AllowedSenderIDs = append([]string(nil), users...)
		}
		if *ttl > 0 {
			cfg.RequestTTL = *ttl
		}
		if *larkCLI != "" {
			cfg.LarkCLI = *larkCLI
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		return writeJSON(stdout, cfg)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

type doctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Warning bool   `json:"warning,omitempty"`
}

func runDoctor(ctx context.Context, output io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	resolved, resolveErr := config.ResolveRuntime(context.Background(), cfg)
	if resolveErr == nil {
		cfg = resolved
	}
	checks := []doctorCheck{{Name: "macOS", OK: runtime.GOOS == "darwin", Detail: runtime.GOOS}}
	commandPaths := make(map[string]string, 3)
	for _, item := range []struct{ name, command string }{{"lark-cli", cfg.LarkCLI}, {"codex", "codex"}, {"claude", "claude"}} {
		path, findErr := exec.LookPath(item.command)
		if findErr == nil {
			commandPaths[item.name] = path
		}
		checks = append(checks, doctorCheck{Name: item.name, OK: findErr == nil, Detail: first(path, errorText(findErr))})
	}
	if codexPath := commandPaths["codex"]; codexPath != "" {
		cwd, cwdErr := os.Getwd()
		doctorCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		report, trustErr := codexhooks.Inspect(doctorCtx, codexPath, cwd)
		cancel()
		checks = append(checks, doctorCheck{
			Name:   "Codex Larky hook trust",
			OK:     cwdErr == nil && trustErr == nil && report.Ready,
			Detail: first(errorText(cwdErr), errorText(trustErr), report.Detail()),
		})
	}
	checks = append(checks,
		doctorCheck{Name: "current Lark user", OK: resolveErr == nil, Detail: errorText(resolveErr)},
		doctorCheck{Name: "notification target", OK: cfg.ChatID != "" || cfg.TargetUserID != "", Detail: first(masked(cfg.ChatID), masked(cfg.TargetUserID))},
		doctorCheck{Name: "allowed sender", OK: len(cfg.AllowedSenderIDs) > 0, Detail: fmt.Sprintf("%d configured", len(cfg.AllowedSenderIDs))},
	)
	away, awayErr := (macos.SystemDetector{}).Detect()
	checks = append(checks, doctorCheck{Name: "away detector", OK: awayErr == nil, Detail: first(away.Method, errorText(awayErr))})
	allOK := true
	for _, check := range checks {
		if !check.OK && !check.Warning {
			allOK = false
		}
	}
	return writeJSON(output, map[string]any{"ok": allOK, "checks": checks, "state_dir": cfg.StateDir})
}

func runHook(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || (args[0] != "stop" && args[0] != "session-start") {
		return errors.New("hook requires stop or session-start")
	}
	hookName := args[0]
	flags := flag.NewFlagSet("hook "+hookName, flag.ContinueOnError)
	platformValue := flags.String("platform", "", "claude or codex")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	platform := contract.Platform(*platformValue)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg, err = config.ResolveRuntime(context.Background(), cfg)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	store := state.New(cfg.DatabasePath())
	service := requestsvc.NewService(store, cfg)
	if hookName == "session-start" {
		decision, err := (hook.SessionStartHandler{Requests: service}).Handle(platform, stdin)
		if err != nil {
			return err
		}
		return writeJSON(stdout, decision)
	}
	handler := hook.StopHandler{
		Config: cfg, Detector: macos.SystemDetector{}, Requests: service, Executable: executable,
		EnsureSidecar: func() error { return sidecar.Ensure(cfg, executable) },
	}
	decision, err := handler.Handle(ctx, platform, stdin)
	if err != nil {
		return err
	}
	return writeJSON(stdout, decision)
}

func runDelivery(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("delivery requires record or fail")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg, err = config.ResolveRuntime(context.Background(), cfg)
	if err != nil {
		return err
	}
	service := requestsvc.NewService(state.New(cfg.DatabasePath()), cfg)
	switch args[0] {
	case "record":
		flags := flag.NewFlagSet("delivery record", flag.ContinueOnError)
		flags.SetOutput(stderr)
		requestID := flags.String("request-id", "", "larky request id")
		messageID := flags.String("message-id", "", "outbound Lark message id")
		chatID := flags.String("chat-id", "", "target Lark chat id")
		identity := flags.String("identity", "", "identity returned by lark-im: bot or user")
		degraded := flags.Bool("degraded", false, "plain-text fallback was used")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := service.RecordDelivery(*requestID, *messageID, *chatID, *identity, *degraded); err != nil {
			return err
		}
		if err := sidecar.Ensure(cfg, ""); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"ok": true, "request_id": strings.ToUpper(*requestID), "message_id": *messageID})
	case "fail":
		flags := flag.NewFlagSet("delivery fail", flag.ContinueOnError)
		flags.SetOutput(stderr)
		requestID := flags.String("request-id", "", "larky request id")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := service.MarkDeliveryFailed(*requestID); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"ok": true, "request_id": strings.ToUpper(*requestID)})
	default:
		return fmt.Errorf("unknown delivery command %q", args[0])
	}
}

func runHandoff(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "show" {
		return errors.New("handoff requires show")
	}
	flags := flag.NewFlagSet("handoff show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requestID := flags.String("request-id", "", "larky request id")
	platformValue := flags.String("platform", "", "claude or codex")
	sessionID := flags.String("session-id", "", "exact agent session id")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("handoff show does not accept positional arguments")
	}
	if *sessionID == "" {
		*sessionID = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	platform := contract.Platform(*platformValue)
	service := requestsvc.NewService(state.New(cfg.DatabasePath()), cfg)
	request, err := service.GetForSession(*requestID, platform, *sessionID)
	if err != nil {
		return err
	}
	if request == nil {
		return errors.New("no request exists for that exact request, platform, and session")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		reply, err := service.GetHandoffReply(*requestID, platform, *sessionID)
		if err != nil {
			return err
		}
		if reply != nil {
			return writeJSON(stdout, reply)
		}
		if time.Now().After(deadline) {
			return errors.New("no handed-off reply exists for that exact request, platform, and session")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func runSidecar(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("sidecar requires run, status, or stop")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "run":
		cfg, err = config.ResolveRuntime(ctx, cfg)
		if err != nil {
			return err
		}
		flags := flag.NewFlagSet("sidecar run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		background := flags.Bool("background-child", false, "internal")
		noEvents := flags.Bool("no-events", false, "disable lark-cli event consumers")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		_ = background
		return sidecar.Run(ctx, cfg, sidecar.Options{DisableEvents: *noEvents, Logger: log.New(stderr, "larky: ", log.LstdFlags|log.Lmicroseconds)})
	case "status":
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status, err := sidecar.GetStatus(requestCtx, cfg)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "stop":
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := sidecar.Stop(requestCtx, cfg); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]bool{"ok": true})
	default:
		return fmt.Errorf("unknown sidecar command %q", args[0])
	}
}

func runSubscribe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("subscribe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	platformValue := flags.String("platform", "", "claude")
	sessionID := flags.String("session-id", "", "exact agent session id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sessionID == "" {
		*sessionID = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg, err = config.ResolveRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	if err := sidecar.Ensure(cfg, ""); err != nil {
		return err
	}
	err = sidecar.Subscribe(ctx, cfg, contract.Platform(*platformValue), *sessionID, stdout)
	if ctx.Err() != nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func runDebug(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "ingest" {
		return errors.New("debug requires ingest")
	}
	flags := flag.NewFlagSet("debug ingest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	eventKey := flags.String("event-key", "", "card.action.trigger or im.message.receive_v1")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4*1024*1024))
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg, err = config.ResolveRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	if err := sidecar.Ensure(cfg, ""); err != nil {
		return err
	}
	reply, err := sidecar.Publish(ctx, cfg, *eventKey, raw, true)
	if err != nil {
		return err
	}
	return writeJSON(stdout, reply)
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("verify requires plan, run, or status")
	}
	root, err := verify.FindRoot(".")
	if err != nil {
		return err
	}
	runner := verify.New(root, stderr)
	switch args[0] {
	case "plan":
		plan, err := runner.Plan()
		if err != nil {
			return err
		}
		return writeJSON(stdout, plan)
	case "status":
		status, err := runner.Status()
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "run":
		flags := flag.NewFlagSet("verify run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		through := flags.Int("through", 3, "highest verification level, 0 through 4")
		var platformValues stringListFlag
		flags.Var(&platformValues, "platform", "live L4 platform (repeatable; defaults to both)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		platforms := make([]contract.Platform, 0, len(platformValues))
		for _, value := range platformValues {
			platform := contract.Platform(value)
			if !platform.Valid() {
				return fmt.Errorf("invalid verification platform %q", value)
			}
			platforms = append(platforms, platform)
		}
		receipt, path, runErr := runner.Run(ctx, verify.RunOptions{Through: *through, LivePlatforms: platforms})
		outputErr := writeJSON(stdout, map[string]any{"receipt": receipt, "receipt_path": path})
		if outputErr != nil {
			return outputErr
		}
		return runErr
	default:
		return fmt.Errorf("unknown verify command %q", args[0])
	}
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func masked(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:4] + "…" + value[len(value)-4:]
}
