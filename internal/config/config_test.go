package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveRuntimeUsesCurrentLarkUserByDefault(t *testing.T) {
	cli := fakeLarkCLI(t, `{"identities":{"user":{"openId":"ou_current"}}}`)
	cfg, err := ResolveRuntime(context.Background(), Config{LarkCLI: cli})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetUserID != "ou_current" {
		t.Fatalf("unexpected target user: %q", cfg.TargetUserID)
	}
	if !reflect.DeepEqual(cfg.AllowedSenderIDs, []string{"ou_current"}) {
		t.Fatalf("unexpected allowed senders: %#v", cfg.AllowedSenderIDs)
	}
}

func TestResolveRuntimeKeepsExplicitTargetAndDefaultsItsSender(t *testing.T) {
	cfg, err := ResolveRuntime(context.Background(), Config{TargetUserID: "ou_explicit", LarkCLI: "/missing/lark-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AllowedSenderIDs, []string{"ou_explicit"}) {
		t.Fatalf("unexpected allowed senders: %#v", cfg.AllowedSenderIDs)
	}
}

func TestResolveRuntimeSupportsEnvelopeStatus(t *testing.T) {
	cli := fakeLarkCLI(t, `{"ok":true,"data":{"identities":{"user":{"openId":"ou_envelope"}}}}`)
	cfg, err := ResolveRuntime(context.Background(), Config{LarkCLI: cli})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetUserID != "ou_envelope" {
		t.Fatalf("unexpected target user: %q", cfg.TargetUserID)
	}
}

func fakeLarkCLI(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lark-cli")
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = \"auth status --json\" ] || exit 2\nprintf '%s\\n' '" + payload + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
