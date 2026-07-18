package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultTTL = 24 * time.Hour

type Config struct {
	ChatID           string        `json:"chat_id"`
	TargetUserID     string        `json:"target_user_id"`
	AllowedSenderIDs []string      `json:"allowed_sender_ids"`
	RequestTTL       time.Duration `json:"request_ttl"`
	LarkCLI          string        `json:"lark_cli"`
	CodexCLI         string        `json:"codex_cli"`
	EventIdentity    string        `json:"event_identity"`
	StateDir         string        `json:"-"`
}

func DefaultStateDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv("LARKY_STATE_DIR")); value != "" {
		return filepath.Clean(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "larky"), nil
}

func Load() (Config, error) {
	stateDir, err := DefaultStateDir()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		RequestTTL:    defaultTTL,
		LarkCLI:       "lark-cli",
		CodexCLI:      "codex",
		EventIdentity: "bot",
		StateDir:      stateDir,
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "config.json"))
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		cfg.StateDir = stateDir
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	applyEnv(&cfg)
	if cfg.RequestTTL <= 0 {
		cfg.RequestTTL = defaultTTL
	}
	if cfg.LarkCLI == "" {
		cfg.LarkCLI = "lark-cli"
	}
	if cfg.CodexCLI == "" {
		cfg.CodexCLI = "codex"
	}
	if cfg.EventIdentity == "" {
		cfg.EventIdentity = "bot"
	}
	return cfg, nil
}

// ResolveRuntime fills the normal single-user defaults from lark-cli's current
// logged-in user. Explicit Larky config and environment overrides still win.
func ResolveRuntime(ctx context.Context, cfg Config) (Config, error) {
	if len(cfg.AllowedSenderIDs) == 0 && cfg.TargetUserID != "" {
		cfg.AllowedSenderIDs = []string{cfg.TargetUserID}
	}
	if (cfg.ChatID != "" || cfg.TargetUserID != "") && len(cfg.AllowedSenderIDs) > 0 {
		return cfg, nil
	}

	openID, err := currentUserOpenID(ctx, cfg.LarkCLI)
	if err != nil {
		return Config{}, fmt.Errorf("discover current Lark user from lark-cli: %w", err)
	}
	if cfg.ChatID == "" && cfg.TargetUserID == "" {
		cfg.TargetUserID = openID
	}
	if len(cfg.AllowedSenderIDs) == 0 {
		cfg.AllowedSenderIDs = []string{openID}
	}
	return cfg, nil
}

func currentUserOpenID(ctx context.Context, cli string) (string, error) {
	if strings.TrimSpace(cli) == "" {
		cli = "lark-cli"
	}
	command := exec.CommandContext(ctx, cli, "auth", "status", "--json")
	command.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run %s auth status --json: %w", cli, err)
	}
	var status struct {
		Identities struct {
			User struct {
				OpenID string `json:"openId"`
			} `json:"user"`
		} `json:"identities"`
		Data struct {
			Identities struct {
				User struct {
					OpenID string `json:"openId"`
				} `json:"user"`
			} `json:"identities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return "", fmt.Errorf("parse lark-cli auth status: %w", err)
	}
	openID := strings.TrimSpace(status.Identities.User.OpenID)
	if openID == "" {
		openID = strings.TrimSpace(status.Data.Identities.User.OpenID)
	}
	if openID == "" {
		return "", errors.New("lark-cli has no logged-in user open_id")
	}
	return openID, nil
}

func Save(cfg Config) error {
	if cfg.StateDir == "" {
		var err error
		cfg.StateDir, err = DefaultStateDir()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(cfg.StateDir, "config.json")
	tmp, err := os.CreateTemp(cfg.StateDir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func (c Config) DatabasePath() string { return filepath.Join(c.StateDir, "state.json") }
func (c Config) SocketPath() string   { return filepath.Join(c.StateDir, "larky.sock") }
func (c Config) LogPath() string      { return filepath.Join(c.StateDir, "sidecar.log") }

func applyEnv(cfg *Config) {
	if value := strings.TrimSpace(os.Getenv("LARKY_CHAT_ID")); value != "" {
		cfg.ChatID = value
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_TARGET_USER_ID")); value != "" {
		cfg.TargetUserID = value
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_ALLOWED_USER_IDS")); value != "" {
		cfg.AllowedSenderIDs = splitList(value)
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_LARK_CLI")); value != "" {
		cfg.LarkCLI = value
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_CODEX_CLI")); value != "" {
		cfg.CodexCLI = value
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_EVENT_IDENTITY")); value != "" {
		cfg.EventIdentity = value
	}
	if value := strings.TrimSpace(os.Getenv("LARKY_REQUEST_TTL")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			cfg.RequestTTL = parsed
		}
	}
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}
