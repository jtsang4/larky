package plugincheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Validate(root string) error {
	checks := []func() error{
		func() error {
			return validateManifest(filepath.Join(root, "plugins/codex/larky/.codex-plugin/plugin.json"), "larky")
		},
		func() error {
			return validateManifest(filepath.Join(root, "plugins/claude/.claude-plugin/plugin.json"), "larky")
		},
		func() error {
			return validateCodexHooks(filepath.Join(root, "plugins/codex/larky/hooks/hooks.json"))
		},
		func() error {
			return validateHooks(filepath.Join(root, "plugins/claude/hooks/hooks.json"), "--platform claude")
		},
		func() error { return validateMonitor(filepath.Join(root, "plugins/claude/monitors/monitors.json")) },
		func() error { return validateSkill(filepath.Join(root, "plugins/codex/larky/skills/larky/SKILL.md")) },
		func() error { return validateSkill(filepath.Join(root, "plugins/claude/skills/larky/SKILL.md")) },
		func() error { return validateExecutable(filepath.Join(root, "plugins/codex/larky/bin/larky")) },
		func() error { return validateExecutable(filepath.Join(root, "plugins/claude/bin/larky")) },
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func validateCodexHooks(path string) error {
	if err := validateHooks(path, "--platform codex"); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.Contains(content, `"SessionStart"`) || !strings.Contains(content, `hook session-start --platform codex`) {
		return errors.New("Codex plugin must recover queued replies in the original task on SessionStart resume")
	}
	if strings.Contains(content, "codex exec resume") {
		return errors.New("Codex plugin must continue through its original Stop hook, not codex exec resume")
	}
	var document struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, group := range document.Hooks["Stop"] {
		for _, handler := range group.Hooks {
			if handler.Timeout < 24*60*60 {
				return errors.New("Codex Stop hook timeout must cover the default 24-hour request lifetime")
			}
		}
	}
	return nil
}

func validateManifest(path, expectedName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, field := range []string{"name", "version", "description"} {
		if strings.TrimSpace(fmt.Sprint(manifest[field])) == "" || manifest[field] == nil {
			return fmt.Errorf("%s: missing %s", path, field)
		}
	}
	if manifest["name"] != expectedName {
		return fmt.Errorf("%s: unexpected plugin name", path)
	}
	return nil
}

func validateHooks(path, platformFragment string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !strings.Contains(string(data), `"Stop"`) || !strings.Contains(string(data), platformFragment) {
		return fmt.Errorf("%s: Stop hook for %s is missing", path, platformFragment)
	}
	return nil
}

func validateMonitor(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var monitors []map[string]any
	if err := json.Unmarshal(data, &monitors); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(monitors) != 1 {
		return errors.New("Claude plugin must define exactly one Larky monitor")
	}
	command := fmt.Sprint(monitors[0]["command"])
	if !strings.Contains(command, "subscribe --platform claude") || strings.Contains(command, "--session-id") {
		return errors.New("Claude monitor must subscribe using CLAUDE_CODE_SESSION_ID from its environment")
	}
	return nil
}

func validateSkill(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\nname: larky\ndescription:") {
		return fmt.Errorf("%s: invalid skill frontmatter", path)
	}
	if strings.Contains(content, "TODO") || !strings.Contains(content, "lark-im") || !strings.Contains(content, "Card 2.0") || !strings.Contains(content, "larky handoff show") || !strings.Contains(content, "remote user cannot see") {
		return fmt.Errorf("%s: skill contract is incomplete", path)
	}
	return nil
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s: launcher is not executable", path)
	}
	return nil
}
