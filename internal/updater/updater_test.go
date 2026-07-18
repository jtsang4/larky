package updater

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunnerDownloadsAndExecutesInstaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/x-shellscript")
		_, _ = writer.Write([]byte("#!/bin/sh\nprintf 'arg=%s\\n' \"$@\"\nprintf 'from=%s\\n' \"$LARKY_UPDATE_FROM_VERSION\"\n"))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runner := Runner{Client: server.Client(), ScriptURL: server.URL}
	if err := runner.Run(context.Background(), "v0.1.0", []string{"--codex", "--version", "v0.2.0"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, expected := range []string{"arg=--update", "arg=--codex", "arg=--version", "arg=v0.2.0", "from=v0.1.0"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output %q does not contain %q", output, expected)
		}
	}
}

func TestRunnerRejectsFailedDownload(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	err := (Runner{Client: server.Client(), ScriptURL: server.URL}).Run(context.Background(), "dev", nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}
