package codexhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectReportsTrustedLarkyHooks(t *testing.T) {
	executable := fakeCodex(t, `[
  {"eventName":"sessionStart","pluginId":"larky@larky","enabled":true,"isManaged":false,"trustStatus":"trusted"},
  {"eventName":"stop","pluginId":"larky@larky","enabled":true,"isManaged":false,"trustStatus":"trusted"},
  {"eventName":"stop","pluginId":"another@plugin","enabled":true,"isManaged":false,"trustStatus":"untrusted"}
]`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := Inspect(ctx, executable, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.Detail() != "sessionStart=trusted, stop=trusted" {
		t.Fatalf("unexpected report: %#v detail=%q", report, report.Detail())
	}
}

func TestInspectExplainsUntrustedOrMissingHooks(t *testing.T) {
	executable := fakeCodex(t, `[
  {"eventName":"session_start","pluginId":"larky@larky","enabled":true,"isManaged":false,"trustStatus":"untrusted"}
]`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := Inspect(ctx, executable, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("untrusted hooks reported ready: %#v", report)
	}
	for _, expected := range []string{"sessionStart=untrusted", "stop=missing", "open /hooks"} {
		if !strings.Contains(report.Detail(), expected) {
			t.Fatalf("detail %q does not contain %q", report.Detail(), expected)
		}
	}
}

func fakeCodex(t *testing.T, hooks string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(hooks)); err != nil {
		t.Fatal(err)
	}
	result := fmt.Sprintf(`{"id":2,"result":{"data":[{"cwd":"/tmp","hooks":%s,"warnings":[],"errors":[]}]}}`, compact.String())
	script := fmt.Sprintf(`#!/bin/sh
set -eu
IFS= read -r initialize
printf '%%s\n' '{"timestamp":"now","level":"WARN"}'
printf '%%s\n' '{"id":1,"result":{"platformOs":"macos"}}'
IFS= read -r initialized
IFS= read -r hooks_list
printf '%%s\n' '%s'
`, result)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
