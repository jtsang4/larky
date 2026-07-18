package verify

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

type LevelPlan struct {
	Level      int      `json:"level"`
	Name       string   `json:"name"`
	Purpose    string   `json:"purpose"`
	Commands   []string `json:"commands,omitempty"`
	NeedsHuman bool     `json:"needs_human,omitempty"`
}

type Plan struct {
	SourceRoot   string      `json:"source_root"`
	SourceDigest string      `json:"source_digest"`
	GeneratedAt  time.Time   `json:"generated_at"`
	Levels       []LevelPlan `json:"levels"`
}

type CommandResult struct {
	Level     int           `json:"level"`
	Name      string        `json:"name"`
	Command   []string      `json:"command"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	ExitCode  int           `json:"exit_code"`
	Passed    bool          `json:"passed"`
	Log       string        `json:"log"`
	Error     string        `json:"error,omitempty"`
}

type LiveEvidence struct {
	Platform        contract.Platform    `json:"platform"`
	RequestID       string               `json:"request_id"`
	EventID         string               `json:"event_id"`
	Action          string               `json:"action"`
	ObservedAt      time.Time            `json:"observed_at"`
	DisplayAsleep   bool                 `json:"display_asleep"`
	ScreenLocked    bool                 `json:"screen_locked"`
	AwayMethod      string               `json:"away_method"`
	HandoffAccepted bool                 `json:"handoff_accepted"`
	HandoffMode     contract.HandoffMode `json:"handoff_mode"`
	HandoffAt       time.Time            `json:"handoff_at"`
}

type Receipt struct {
	RunID        string          `json:"run_id"`
	SourceDigest string          `json:"source_digest"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Through      int             `json:"through"`
	Passed       bool            `json:"passed"`
	Results      []CommandResult `json:"results"`
	Live         []LiveEvidence  `json:"live_evidence,omitempty"`
}

type Status struct {
	OK            bool     `json:"ok"`
	Fresh         bool     `json:"fresh"`
	CurrentDigest string   `json:"current_digest"`
	Receipt       *Receipt `json:"receipt,omitempty"`
	ReceiptPath   string   `json:"receipt_path,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

type Runner struct {
	Root      string
	Artifacts string
	Output    io.Writer
}

type RunOptions struct {
	Through       int
	LivePlatforms []contract.Platform
}

func FindRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(current); err == nil && !info.IsDir() {
		current = filepath.Dir(current)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not find larky go.mod")
		}
		current = parent
	}
}

func New(root string, output io.Writer) *Runner {
	return &Runner{Root: root, Artifacts: filepath.Join(root, ".artifacts", "verification"), Output: output}
}

func (r *Runner) Plan() (Plan, error) {
	digest, err := SourceDigest(r.Root)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		SourceRoot: r.Root, SourceDigest: digest, GeneratedAt: time.Now().UTC(),
		Levels: []LevelPlan{
			{Level: 0, Name: "contract", Purpose: "format, schemas, parsers, and plugin contracts", Commands: []string{"gofmt -l cmd internal", "go test ./internal/contract ./internal/larkevent ./internal/plugincheck"}},
			{Level: 1, Name: "functional", Purpose: "all deterministic unit and component tests", Commands: []string{"go test ./..."}},
			{Level: 2, Name: "concurrency", Purpose: "static analysis and race detection", Commands: []string{"go vet ./...", "go test -race ./..."}},
			{Level: 3, Name: "host-e2e", Purpose: "built binary, event subprocess, exact-session routing, live rebuild, and host plugin validation", Commands: []string{"make build", "go test ./internal/integration -count=1", "LARKY_ATOMIC_REBUILD_TEST=1 go test ./internal/integration -run TestBuiltBinaryIsAtomicallyReplacedWhileSidecarRuns -count=1", "claude plugin validate plugins/claude --strict"}},
			{Level: 4, Name: "live-e2e", Purpose: "real CoreGraphics away state, real Lark Card 2.0 callback, and exact host session", NeedsHuman: true},
		},
	}, nil
}

func (r *Runner) Run(ctx context.Context, options RunOptions) (Receipt, string, error) {
	if options.Through < 0 || options.Through > 4 {
		return Receipt{}, "", errors.New("through must be between 0 and 4")
	}
	plan, err := r.Plan()
	if err != nil {
		return Receipt{}, "", err
	}
	started := time.Now().UTC()
	runID := started.Format("20060102T150405Z") + "-" + plan.SourceDigest[:8]
	runDir := filepath.Join(r.Artifacts, runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return Receipt{}, "", err
	}
	receipt := Receipt{RunID: runID, SourceDigest: plan.SourceDigest, StartedAt: started, Through: options.Through, Passed: true}
	commands := r.commands(runDir, options.Through)
	for index, command := range commands {
		result := r.runCommand(ctx, runDir, index, command)
		receipt.Results = append(receipt.Results, result)
		if !result.Passed {
			receipt.Passed = false
			break
		}
	}
	if receipt.Passed && options.Through >= 4 {
		platforms := options.LivePlatforms
		if len(platforms) == 0 {
			platforms = []contract.Platform{contract.PlatformClaude, contract.PlatformCodex}
		}
		cfg, loadErr := config.Load()
		if loadErr != nil {
			receipt.Passed = false
			err = loadErr
		} else {
			for _, platform := range platforms {
				evidence, evidenceErr := LiveCheck(state.New(cfg.DatabasePath()), platform, started.Add(-2*time.Hour))
				if evidenceErr != nil {
					receipt.Passed = false
					err = evidenceErr
					break
				}
				receipt.Live = append(receipt.Live, evidence)
			}
		}
	}
	receipt.FinishedAt = time.Now().UTC()
	receiptPath := filepath.Join(runDir, "receipt.json")
	if writeErr := writeJSONFile(receiptPath, receipt); writeErr != nil {
		return receipt, receiptPath, writeErr
	}
	if !receipt.Passed {
		if err == nil {
			err = errors.New("verification command failed")
		}
		return receipt, receiptPath, err
	}
	return receipt, receiptPath, nil
}

type commandSpec struct {
	level        int
	name         string
	args         []string
	requireEmpty bool
}

func (r *Runner) commands(runDir string, through int) []commandSpec {
	formatArgs := []string{"gofmt", "-l"}
	if files, err := goFiles(r.Root); err == nil {
		formatArgs = append(formatArgs, files...)
	}
	all := []commandSpec{
		{0, "gofmt", formatArgs, true},
		{0, "contract-tests", []string{"go", "test", "./internal/contract", "./internal/larkevent", "./internal/plugincheck"}, false},
		{1, "all-tests", []string{"go", "test", "./..."}, false},
		{2, "go-vet", []string{"go", "vet", "./..."}, false},
		{2, "race-tests", []string{"go", "test", "-race", "./..."}, false},
		{3, "build", []string{"make", "build"}, false},
		{3, "integration", []string{"go", "test", "./internal/integration", "-count=1"}, false},
		{3, "atomic-rebuild", []string{"env", "LARKY_ATOMIC_REBUILD_TEST=1", "go", "test", "./internal/integration", "-run", "TestBuiltBinaryIsAtomicallyReplacedWhileSidecarRuns", "-count=1"}, false},
		{3, "claude-plugin", []string{"claude", "plugin", "validate", "plugins/claude", "--strict"}, false},
	}
	result := make([]commandSpec, 0, len(all))
	for _, item := range all {
		if item.level <= through {
			result = append(result, item)
		}
	}
	return result
}

func goFiles(root string) ([]string, error) {
	var files []string
	for _, directory := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func (r *Runner) runCommand(ctx context.Context, runDir string, index int, spec commandSpec) CommandResult {
	started := time.Now().UTC()
	logName := fmt.Sprintf("%02d-L%d-%s.log", index+1, spec.level, spec.name)
	logPath := filepath.Join(runDir, logName)
	result := CommandResult{Level: spec.level, Name: spec.name, Command: spec.args, StartedAt: started, ExitCode: -1, Log: logName}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer logFile.Close()
	if r.Output != nil {
		fmt.Fprintf(r.Output, "L%d %s: %s\n", spec.level, spec.name, strings.Join(spec.args, " "))
	}
	cmd := exec.CommandContext(ctx, spec.args[0], spec.args[1:]...)
	cmd.Dir = r.Root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	err = cmd.Run()
	result.Duration = time.Since(started)
	if err == nil {
		result.ExitCode = 0
		if spec.requireEmpty {
			if info, statErr := logFile.Stat(); statErr != nil || info.Size() != 0 {
				result.Error = "formatter reported files requiring changes"
				return result
			}
		}
		result.Passed = true
		return result
	}
	result.Error = err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func (r *Runner) Status() (Status, error) {
	digest, err := SourceDigest(r.Root)
	if err != nil {
		return Status{}, err
	}
	path, receipt, err := latestReceipt(r.Artifacts)
	if errors.Is(err, os.ErrNotExist) {
		return Status{CurrentDigest: digest, Reason: "no verification receipt"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	fresh := receipt.SourceDigest == digest
	status := Status{OK: receipt.Passed && fresh, Fresh: fresh, CurrentDigest: digest, Receipt: &receipt, ReceiptPath: path}
	if !receipt.Passed {
		status.Reason = "latest verification failed"
	} else if !fresh {
		status.Reason = "source changed after the latest verification"
	}
	return status, nil
}

func SourceDigest(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel == ".git" || rel == "dist" || rel == ".artifacts" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", filepath.ToSlash(rel), info.Mode().Perm(), len(data))
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func LiveCheck(store *state.Store, platform contract.Platform, since time.Time) (LiveEvidence, error) {
	if !platform.Valid() {
		return LiveEvidence{}, errors.New("invalid live verification platform")
	}
	var best *LiveEvidence
	expectedHandoff := contract.HandoffClaudeMonitor
	if platform == contract.PlatformCodex {
		expectedHandoff = contract.HandoffCodexStopHook
	}
	err := store.View(func(db *state.Database) error {
		verification := make(map[string]contract.IncomingEvent)
		for _, event := range db.Verification {
			verification[event.EventID] = event
		}
		for _, req := range db.Requests {
			if req.Platform != platform || !req.AwayDetected || req.AwayMethod != "coregraphics" || req.MessageID == "" || req.DegradedDelivery || req.ClaimedEventID == "" {
				continue
			}
			if req.State != contract.StateResumed || req.HandoffMode != expectedHandoff || req.HandoffEventID != req.ClaimedEventID || req.HandoffAt.Before(since) {
				continue
			}
			processed, ok := db.Events[req.ClaimedEventID]
			if !ok || processed.SeenAt.Before(since) || processed.Synthetic || processed.Source != "lark-live" {
				continue
			}
			event, ok := verification[req.ClaimedEventID]
			if !ok || event.Kind != contract.IncomingCardAction || event.Synthetic {
				continue
			}
			if !liveAction(event.Action) || replyQueued(db, req, event.EventID) {
				continue
			}
			candidate := &LiveEvidence{
				Platform: platform, RequestID: req.ID, EventID: req.ClaimedEventID, Action: event.Action, ObservedAt: processed.SeenAt,
				DisplayAsleep: req.DisplayAsleep, ScreenLocked: req.ScreenLocked, AwayMethod: req.AwayMethod,
				HandoffAccepted: true, HandoffMode: req.HandoffMode, HandoffAt: req.HandoffAt,
			}
			if best == nil || candidate.ObservedAt.After(best.ObservedAt) {
				best = candidate
			}
		}
		return nil
	})
	if err != nil {
		return LiveEvidence{}, err
	}
	if best == nil {
		return LiveEvidence{}, fmt.Errorf("no fresh non-synthetic Card 2.0 callback and exact-host handoff evidence for %s; lock the Mac, let the real Stop hook send a card, then tap a non-terminal action", platform)
	}
	return *best, nil
}

func liveAction(action string) bool {
	switch action {
	case "continue", "retry", "answer", "submit_context":
		return true
	default:
		return false
	}
}

func replyQueued(db *state.Database, req *contract.InteractionRequest, eventID string) bool {
	for _, item := range db.Inbox[state.InboxKey(req.Platform, req.SessionID)] {
		if item.Reply.EventID == eventID {
			return true
		}
	}
	return false
}

func latestReceipt(root string) (string, Receipt, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", Receipt{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "receipt.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var receipt Receipt
		if json.Unmarshal(data, &receipt) == nil {
			return path, receipt, nil
		}
	}
	return "", Receipt{}, os.ErrNotExist
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func TailLog(path string, lines int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var kept []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		kept = append(kept, scanner.Text())
		if len(kept) > lines {
			kept = kept[1:]
		}
	}
	return strings.Join(kept, "\n"), scanner.Err()
}

func ParseThrough(value string) (int, error) {
	level, err := strconv.Atoi(value)
	if err != nil || level < 0 || level > 4 {
		return 0, errors.New("verification level must be 0, 1, 2, 3, or 4")
	}
	return level, nil
}
