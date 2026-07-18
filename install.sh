#!/bin/sh
set -eu

repository="jtsang4/larky"
requested_version="latest"
explicit_selection=0
want_claude=0
want_codex=0
binary_only=0

usage() {
  cat <<'EOF'
Install or update Larky on macOS.

Usage:
  install.sh [--version <vX.Y.Z>] [--claude] [--codex] [--all] [--binary-only]

With no plugin flag, the installer detects Claude Code and Codex on PATH and
installs Larky for every detected host. Re-running this script upgrades the
binary and those plugins through the same verified release path.

Environment overrides:
  LARKY_INSTALL_DIR       Release storage (default: ~/.local/share/larky)
  LARKY_BIN_DIR           Command directory (default: ~/.local/bin)
  LARKY_RELEASE_BASE_URL  Exact asset directory URL (for mirrors/testing)
EOF
}

say() {
  printf '%s\n' "$*"
}

die() {
  printf 'larky: %s\n' "$*" >&2
  exit 1
}

need_value() {
  [ "$#" -ge 2 ] || die "$1 requires a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      need_value "$@"
      requested_version=$2
      shift 2
      ;;
    --claude)
      explicit_selection=1
      want_claude=1
      shift
      ;;
    --codex)
      explicit_selection=1
      want_codex=1
      shift
      ;;
    --all)
      explicit_selection=1
      want_claude=1
      want_codex=1
      shift
      ;;
    --binary-only)
      explicit_selection=1
      binary_only=1
      shift
      ;;
    --update)
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || die "only macOS is supported"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
[ -n "${HOME:-}" ] || die "HOME is not set"

if [ "$binary_only" -eq 1 ] && { [ "$want_claude" -eq 1 ] || [ "$want_codex" -eq 1 ]; }; then
  die "--binary-only cannot be combined with plugin selection flags"
fi

if [ "$explicit_selection" -eq 0 ]; then
  if command -v claude >/dev/null 2>&1; then
    want_claude=1
  fi
  if command -v codex >/dev/null 2>&1; then
    want_codex=1
  fi
fi

if [ "$want_claude" -eq 1 ] && ! command -v claude >/dev/null 2>&1; then
  die "Claude Code was selected but the claude command is unavailable"
fi
if [ "$want_codex" -eq 1 ] && ! command -v codex >/dev/null 2>&1; then
  die "Codex was selected but the codex command is unavailable"
fi

machine_arch=$(uname -m)
case "$machine_arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) die "unsupported architecture: $machine_arch" ;;
esac

install_root=${LARKY_INSTALL_DIR:-"$HOME/.local/share/larky"}
bin_dir=${LARKY_BIN_DIR:-"$HOME/.local/bin"}
case "$install_root" in
  ""|/|"$HOME") die "unsafe LARKY_INSTALL_DIR: $install_root" ;;
esac
case "$bin_dir" in
  ""|/) die "unsafe LARKY_BIN_DIR: $bin_dir" ;;
esac

if [ -n "${LARKY_RELEASE_BASE_URL:-}" ]; then
  asset_base=${LARKY_RELEASE_BASE_URL%/}
elif [ "$requested_version" = "latest" ]; then
  asset_base="https://github.com/$repository/releases/latest/download"
else
  case "$requested_version" in
    v*) release_tag=$requested_version ;;
    *) release_tag="v$requested_version" ;;
  esac
  case "$release_tag" in
    *[!A-Za-z0-9._+-]*) die "invalid release version: $requested_version" ;;
  esac
  asset_base="https://github.com/$repository/releases/download/$release_tag"
fi

temporary=$(mktemp -d "${TMPDIR:-/tmp}/larky-install.XXXXXX")
trap 'rm -rf "$temporary"' EXIT INT TERM HUP

download() {
  source_url=$1
  destination=$2
  curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 \
    "$source_url" --output "$destination"
}

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    die "shasum or sha256sum is required"
  fi
}

download "$asset_base/checksums.txt" "$temporary/checksums.txt"

download_asset() {
  asset=$1
  destination="$temporary/$asset"
  expected=$(awk -v name="$asset" '{ file=$2; sub(/^\*/, "", file); if (file == name) { print $1; exit } }' "$temporary/checksums.txt")
  [ -n "$expected" ] || die "checksums.txt has no entry for $asset"
  download "$asset_base/$asset" "$destination"
  actual=$(sha256_file "$destination")
  [ "$actual" = "$expected" ] || die "checksum mismatch for $asset"
}

binary_asset="larky-darwin-$arch.tar.gz"
claude_asset="larky-claude-darwin-$arch.tar.gz"
codex_asset="larky-codex-darwin-$arch.tar.gz"

say "Downloading verified Larky release assets for darwin/$arch..."
download_asset "$binary_asset"
download_asset "$claude_asset"
download_asset "$codex_asset"

stage="$temporary/release"
mkdir -p "$stage/bin" "$stage/claude" "$stage/codex"
tar -xzf "$temporary/$binary_asset" -C "$stage/bin"
tar -xzf "$temporary/$claude_asset" -C "$stage/claude"
tar -xzf "$temporary/$codex_asset" -C "$stage/codex"
mv "$stage/bin/larky-darwin-$arch" "$stage/bin/larky"
chmod 755 "$stage/bin/larky"

[ -x "$stage/bin/larky" ] || die "release binary is missing"
[ -f "$stage/claude/.claude-plugin/marketplace.json" ] || die "Claude marketplace is missing"
[ -f "$stage/codex/.agents/plugins/marketplace.json" ] || die "Codex marketplace is missing"

installed_version=$("$stage/bin/larky" version)
version_key=$(printf '%s' "$installed_version" | tr -c 'A-Za-z0-9._+-' '-')
[ -n "$version_key" ] || die "release binary returned an invalid version"
manifest_digest=$(sha256_file "$temporary/checksums.txt" | cut -c1-12)
release_id="$version_key-$manifest_digest"
release_dir="$install_root/releases/$release_id"
current_link="$install_root/current"

mkdir -p "$install_root/releases" "$install_root/marketplaces/claude" "$install_root/marketplaces/codex" "$bin_dir"

if [ -x "$current_link/bin/larky" ]; then
  "$current_link/bin/larky" sidecar stop >/dev/null 2>&1 || true
fi

if [ ! -d "$release_dir" ]; then
  mv "$stage" "$release_dir"
fi

next_link="$temporary/current"
ln -s "$release_dir" "$next_link"
mv -fh "$next_link" "$current_link"
ln -sfn "$current_link/bin/larky" "$bin_dir/larky"

claude_marketplace="$install_root/marketplaces/claude"
codex_marketplace="$install_root/marketplaces/codex"
ln -sfn "$current_link/claude/.claude-plugin" "$claude_marketplace/.claude-plugin"
ln -sfn "$current_link/claude/plugins" "$claude_marketplace/plugins"
ln -sfn "$current_link/codex/.agents" "$codex_marketplace/.agents"
ln -sfn "$current_link/codex/plugins" "$codex_marketplace/plugins"

if [ "$want_claude" -eq 1 ]; then
  say "Installing or updating the Claude Code plugin..."
  claude_marketplaces="$temporary/claude-marketplaces.json"
  claude plugin marketplace list --json > "$claude_marketplaces"
  configured_claude_root=$(awk '
    /"name"[[:space:]]*:[[:space:]]*"larky"/ { found=1; next }
    found && /"path"[[:space:]]*:/ {
      line=$0
      sub(/^.*"path"[[:space:]]*:[[:space:]]*"/, "", line)
      sub(/".*$/, "", line)
      print line
      exit
    }
  ' "$claude_marketplaces")
  if grep -q '"name"[[:space:]]*:[[:space:]]*"larky"' "$claude_marketplaces" && [ "$configured_claude_root" != "$claude_marketplace" ]; then
    claude plugin marketplace remove larky >/dev/null
  fi
  claude plugin marketplace add "$claude_marketplace" --scope user >/dev/null
  claude plugin marketplace update larky >/dev/null
  if claude plugin list --json 2>/dev/null | grep -q '"id"[[:space:]]*:[[:space:]]*"larky@larky"'; then
    claude plugin update larky@larky --scope user >/dev/null
  else
    claude plugin install larky@larky --scope user >/dev/null
  fi
fi

if [ "$want_codex" -eq 1 ]; then
  say "Installing or updating the Codex plugin..."
  configured_codex_root=$(codex plugin marketplace list | awk '$1 == "larky" { print $2; exit }')
  expected_codex_root=$(CDPATH= cd -- "$codex_marketplace" && pwd -P)
  if [ -n "$configured_codex_root" ] && [ "$configured_codex_root" != "$expected_codex_root" ]; then
    codex plugin marketplace remove larky >/dev/null
  fi
  codex plugin marketplace add "$codex_marketplace" --json >/dev/null
  codex plugin marketplace upgrade larky >/dev/null 2>&1 || true
  codex plugin add larky@larky --json >/dev/null
fi

say "Larky $installed_version is installed."
say "Command: $bin_dir/larky"
if [ "$want_claude" -eq 0 ] && [ "$want_codex" -eq 0 ]; then
  say "No supported coding-agent CLI was detected; only the command was installed."
fi
case ":${PATH:-}:" in
  *:"$bin_dir":*) ;;
  *) say "Add $bin_dir to PATH, for example: export PATH=\"$bin_dir:\$PATH\"" ;;
esac
if [ "$want_claude" -eq 1 ] || [ "$want_codex" -eq 1 ]; then
  say "Restart the coding-agent app or start a new task to load the updated plugin."
fi
