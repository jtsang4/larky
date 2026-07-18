#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
arch=$(go env GOARCH)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/larky-install-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT INT TERM HUP

assets="$test_root/assets"
update_assets="$test_root/update-assets"
fake_bin="$test_root/fake-bin"
install_root="$test_root/install"
command_dir="$test_root/commands"
host_log="$test_root/hosts.log"
mkdir -p "$assets" "$update_assets" "$fake_bin" "$command_dir"

cp "$repo_root/dist/larky-darwin-$arch.tar.gz" "$assets/"
cp "$repo_root/dist/larky-claude-darwin-$arch.tar.gz" "$assets/"
cp "$repo_root/dist/larky-codex-darwin-$arch.tar.gz" "$assets/"
(
  cd "$assets"
  shasum -a 256 larky-*.tar.gz > checksums.txt
)

cat > "$fake_bin/claude" <<'EOF'
#!/bin/sh
set -eu
printf 'claude %s\n' "$*" >> "$LARKY_TEST_HOST_LOG"
case "$*" in
  "plugin list --json")
    if [ -f "$LARKY_TEST_CLAUDE_INSTALLED" ]; then
      printf '[{"id":"larky@larky","scope":"user"}]\n'
    else
      printf '[]\n'
    fi
    ;;
  *"plugin install larky@larky"*)
    : > "$LARKY_TEST_CLAUDE_INSTALLED"
    ;;
esac
EOF

cat > "$fake_bin/codex" <<'EOF'
#!/bin/sh
set -eu
printf 'codex %s\n' "$*" >> "$LARKY_TEST_HOST_LOG"
EOF
chmod 755 "$fake_bin/claude" "$fake_bin/codex"

run_installer() {
  selected_assets=$1
  HOME="$test_root/home" \
  PATH="$fake_bin:/usr/bin:/bin:/usr/sbin:/sbin" \
  LARKY_INSTALL_DIR="$install_root" \
  LARKY_BIN_DIR="$command_dir" \
  LARKY_RELEASE_BASE_URL="file://$selected_assets" \
  LARKY_TEST_HOST_LOG="$host_log" \
  LARKY_TEST_CLAUDE_INSTALLED="$test_root/claude-installed" \
    /bin/sh "$repo_root/install.sh" --all
}

run_installer "$assets"
first_version=$("$command_dir/larky" version)
first_target=$(readlink "$install_root/current")

make -s -C "$repo_root" package VERSION=v99.99.99-install-test >/dev/null
cp "$repo_root/dist/larky-darwin-$arch.tar.gz" "$update_assets/"
cp "$repo_root/dist/larky-claude-darwin-$arch.tar.gz" "$update_assets/"
cp "$repo_root/dist/larky-codex-darwin-$arch.tar.gz" "$update_assets/"
(
  cd "$update_assets"
  shasum -a 256 larky-*.tar.gz > checksums.txt
)

run_installer "$update_assets"
second_version=$("$command_dir/larky" version)
second_target=$(readlink "$install_root/current")
run_installer "$update_assets"
third_version=$("$command_dir/larky" version)

corrupt_assets="$test_root/corrupt-assets"
cp -R "$update_assets" "$corrupt_assets"
printf 'corrupt\n' >> "$corrupt_assets/larky-darwin-$arch.tar.gz"
if HOME="$test_root/home" \
  PATH="$fake_bin:/usr/bin:/bin:/usr/sbin:/sbin" \
  LARKY_INSTALL_DIR="$install_root" \
  LARKY_BIN_DIR="$command_dir" \
  LARKY_RELEASE_BASE_URL="file://$corrupt_assets" \
    /bin/sh "$repo_root/install.sh" --binary-only >/dev/null 2>&1
then
  echo "larky installer test: corrupted asset was accepted" >&2
  exit 1
fi

[ "$first_version" != "$second_version" ] || {
  echo "larky installer test: version did not change during update" >&2
  exit 1
}
[ "$second_version" = "$third_version" ] || {
  echo "larky installer test: repeated update is not idempotent" >&2
  exit 1
}
[ "$("$command_dir/larky" version)" = "$third_version" ] || {
  echo "larky installer test: failed update changed the active release" >&2
  exit 1
}
[ "$first_target" != "$second_target" ] || {
  echo "larky installer test: current release link did not advance" >&2
  exit 1
}
[ -L "$install_root/current" ] || {
  echo "larky installer test: current release link is missing" >&2
  exit 1
}
[ -L "$command_dir/larky" ] || {
  echo "larky installer test: command link is missing" >&2
  exit 1
}
[ -f "$install_root/marketplaces/claude/.claude-plugin/marketplace.json" ] || {
  echo "larky installer test: Claude marketplace is missing" >&2
  exit 1
}
[ -f "$install_root/marketplaces/codex/.agents/plugins/marketplace.json" ] || {
  echo "larky installer test: Codex marketplace is missing" >&2
  exit 1
}
grep -q 'claude plugin install larky@larky --scope user' "$host_log"
grep -q 'claude plugin update larky@larky --scope user' "$host_log"
grep -q 'codex plugin add larky@larky --json' "$host_log"

printf 'installer update test passed: %s -> %s (%s)\n' "$first_version" "$second_version" "$arch"
