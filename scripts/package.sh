#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
arch=$(go env GOARCH)
binary="$repo_root/dist/larky-darwin-$arch"

if [ ! -x "$binary" ]; then
  echo "larky: missing $binary; run make build first" >&2
  exit 1
fi

stage=$(mktemp -d "${TMPDIR:-/tmp}/larky-package.XXXXXX")
trap 'rm -rf "$stage"' EXIT INT TERM

mkdir -p "$stage/claude/plugins" "$stage/claude/.claude-plugin" "$stage/codex/plugins" "$stage/codex/.agents/plugins"
cp -R "$repo_root/plugins/claude" "$stage/claude/plugins/larky"
cp "$binary" "$stage/claude/plugins/larky/bin/larky-darwin-$arch"
cp "$repo_root/packaging/claude-marketplace.json" "$stage/claude/.claude-plugin/marketplace.json"

cp -R "$repo_root/plugins/codex/larky" "$stage/codex/plugins/larky"
cp "$binary" "$stage/codex/plugins/larky/bin/larky-darwin-$arch"
cp "$repo_root/packaging/codex-marketplace.json" "$stage/codex/.agents/plugins/marketplace.json"

mkdir -p "$repo_root/dist"
tar -czf "$repo_root/dist/larky-claude-darwin-$arch.tar.gz" -C "$stage/claude" .
tar -czf "$repo_root/dist/larky-codex-darwin-$arch.tar.gz" -C "$stage/codex" .
tar -czf "$repo_root/dist/larky-darwin-$arch.tar.gz" -C "$repo_root/dist" "larky-darwin-$arch"

printf '%s\n' \
  "$repo_root/dist/larky-claude-darwin-$arch.tar.gz" \
  "$repo_root/dist/larky-codex-darwin-$arch.tar.gz" \
  "$repo_root/dist/larky-darwin-$arch.tar.gz"
