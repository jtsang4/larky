package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateDelegatesToSharedInstaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"))
	}))
	defer server.Close()
	t.Setenv("LARKY_INSTALL_SCRIPT_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"update", "--codex", "--version", "v0.2.0"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"--update", "--codex", "--version", "v0.2.0"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output %q does not contain %q", stdout.String(), expected)
		}
	}
}

func TestUpdateRejectsConflictingSelection(t *testing.T) {
	err := Run(context.Background(), []string{"update", "--all", "--binary-only"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected selection conflict, got %v", err)
	}
}
