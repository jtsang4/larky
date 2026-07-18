package codexhooks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const pluginID = "larky@larky"

var requiredEvents = []string{"sessionStart", "stop"}

// Hook is the subset of a hooks/list entry needed to verify whether Codex can
// execute Larky's lifecycle hooks.
type Hook struct {
	EventName   string `json:"eventName"`
	PluginID    string `json:"pluginId"`
	Enabled     bool   `json:"enabled"`
	IsManaged   bool   `json:"isManaged"`
	TrustStatus string `json:"trustStatus"`
}

// Report describes the effective Larky hook state returned by Codex.
type Report struct {
	Ready  bool
	States map[string]string
}

// Detail returns a compact diagnostic with the native Codex remediation when
// either required hook is not ready.
func (r Report) Detail() string {
	parts := make([]string, 0, len(requiredEvents))
	for _, event := range requiredEvents {
		state := r.States[event]
		if state == "" {
			state = "missing"
		}
		parts = append(parts, event+"="+state)
	}
	detail := strings.Join(parts, ", ")
	if !r.Ready {
		detail += "; restart Codex, open /hooks, and trust both Larky hooks"
	}
	return detail
}

// Inspect asks Codex's local app-server for the effective lifecycle hooks in
// cwd. It is deliberately read-only: trust remains an explicit Codex UI
// decision made through /hooks.
func Inspect(ctx context.Context, executable, cwd string) (Report, error) {
	if strings.TrimSpace(executable) == "" {
		return Report{}, errors.New("codex executable is empty")
	}

	command := exec.CommandContext(ctx, executable, "app-server", "--stdio")
	if cwd != "" {
		command.Dir = cwd
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return Report{}, fmt.Errorf("open Codex stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Report{}, fmt.Errorf("open Codex stdout: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return Report{}, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if err := encoder.Encode(map[string]any{
		"method": "initialize",
		"id":     1,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name": "larky_doctor", "title": "Larky Doctor", "version": "1",
			},
			"capabilities": map[string]bool{"experimentalApi": true},
		},
	}); err != nil {
		return Report{}, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if _, err := readResponse(ctx, scanner, 1, &stderr); err != nil {
		return Report{}, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return Report{}, fmt.Errorf("acknowledge Codex initialization: %w", err)
	}
	if err := encoder.Encode(map[string]any{
		"method": "hooks/list",
		"id":     2,
		"params": map[string]string{"cwd": cwd},
	}); err != nil {
		return Report{}, fmt.Errorf("request Codex hooks: %w", err)
	}
	response, err := readResponse(ctx, scanner, 2, &stderr)
	if err != nil {
		return Report{}, err
	}

	var result struct {
		Data []struct {
			Hooks []Hook `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return Report{}, fmt.Errorf("decode Codex hooks/list result: %w", err)
	}
	report := Report{States: make(map[string]string, len(requiredEvents))}
	for _, item := range result.Data {
		for _, candidate := range item.Hooks {
			if candidate.PluginID != pluginID {
				continue
			}
			event := canonicalEvent(candidate.EventName)
			if event == "" {
				continue
			}
			state := hookState(candidate)
			if report.States[event] == "trusted" {
				continue
			}
			report.States[event] = state
		}
	}
	report.Ready = true
	for _, event := range requiredEvents {
		if report.States[event] != "trusted" {
			report.Ready = false
		}
	}
	return report, nil
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func readResponse(ctx context.Context, scanner *bufio.Scanner, wantedID int, stderr *bytes.Buffer) (rpcResponse, error) {
	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.ID) == 0 {
			continue
		}
		var id int
		if err := json.Unmarshal(response.ID, &id); err != nil || id != wantedID {
			continue
		}
		if response.Error != nil {
			return rpcResponse{}, fmt.Errorf("Codex app-server error %d: %s", response.Error.Code, response.Error.Message)
		}
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		return rpcResponse{}, fmt.Errorf("Codex app-server timed out: %w", err)
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, fmt.Errorf("read Codex app-server: %w", err)
	}
	detail := strings.TrimSpace(stderr.String())
	if detail != "" {
		return rpcResponse{}, fmt.Errorf("Codex app-server closed before response %d: %s", wantedID, detail)
	}
	return rpcResponse{}, fmt.Errorf("Codex app-server closed before response %d", wantedID)
}

func canonicalEvent(value string) string {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(value))
	switch normalized {
	case "sessionstart":
		return "sessionStart"
	case "stop":
		return "stop"
	default:
		return ""
	}
}

func hookState(hook Hook) string {
	if !hook.Enabled {
		return "disabled"
	}
	if hook.IsManaged || strings.EqualFold(hook.TrustStatus, "trusted") || strings.EqualFold(hook.TrustStatus, "managed") {
		return "trusted"
	}
	if strings.TrimSpace(hook.TrustStatus) == "" {
		return "untrusted"
	}
	return strings.ToLower(hook.TrustStatus)
}
