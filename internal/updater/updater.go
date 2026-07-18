package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const DefaultScriptURL = "https://raw.githubusercontent.com/jtsang4/larky/main/install.sh"

const maxScriptSize = 2 << 20

type Runner struct {
	Client    *http.Client
	ScriptURL string
	Shell     string
}

func (r Runner) Run(ctx context.Context, currentVersion string, args []string, stdout, stderr io.Writer) error {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	scriptURL := strings.TrimSpace(r.ScriptURL)
	if scriptURL == "" {
		scriptURL = DefaultScriptURL
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scriptURL, nil)
	if err != nil {
		return fmt.Errorf("prepare installer download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download installer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download installer: %s", response.Status)
	}

	temporary, err := os.CreateTemp("", "larky-install-*.sh")
	if err != nil {
		return fmt.Errorf("create installer file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	written, copyErr := io.Copy(temporary, io.LimitReader(response.Body, maxScriptSize+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("save installer: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("save installer: %w", closeErr)
	}
	if written == 0 {
		return fmt.Errorf("download installer: empty response")
	}
	if written > maxScriptSize {
		return fmt.Errorf("download installer: response exceeds %d bytes", maxScriptSize)
	}
	if err := os.Chmod(temporaryPath, 0o700); err != nil {
		return fmt.Errorf("make installer executable: %w", err)
	}

	shell := strings.TrimSpace(r.Shell)
	if shell == "" {
		shell = "/bin/sh"
	}
	commandArgs := append([]string{temporaryPath, "--update"}, args...)
	command := exec.CommandContext(ctx, shell, commandArgs...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Stdin = nil
	command.Env = withEnvironment(os.Environ(), "LARKY_UPDATE_FROM_VERSION", currentVersion)
	if err := command.Run(); err != nil {
		return fmt.Errorf("installer failed: %w", err)
	}
	return nil
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
