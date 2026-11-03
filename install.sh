#!/usr/bin/env bash
set -euo pipefail

readonly RELEASE_BASE_URL="https://github.com/mlahr/git-cleaner/releases/latest/download"

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

need_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported operating system: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

need_command curl
need_command tar
need_command awk
if command -v sha256sum >/dev/null 2>&1; then
  checksum_command=(sha256sum -c)
elif command -v shasum >/dev/null 2>&1; then
  checksum_command=(shasum -a 256 -c)
else
  die "required command not found: sha256sum or shasum"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
curl -fsSL "$RELEASE_BASE_URL/checksums.txt" -o "$tmp_dir/checksums.txt"
asset_name="$(awk -v suffix="_${os}_${arch}.tar.gz" 'length($2) >= length(suffix) && substr($2, length($2) - length(suffix) + 1) == suffix { print $2 }' "$tmp_dir/checksums.txt")"
[ -n "$asset_name" ] || die "no ${os} ${arch} archive found in latest release checksums.txt"
case "$asset_name" in
  *$'\n'*|*/*|*\\*) die "invalid or multiple archive names in checksums.txt" ;;
esac
curl -fsSL "$RELEASE_BASE_URL/$asset_name" -o "$tmp_dir/$asset_name"
(
  cd "$tmp_dir"
  awk -v name="$asset_name" '$2 == name { print }' checksums.txt | "${checksum_command[@]}" -
)
tar -xzf "$tmp_dir/$asset_name" -C "$tmp_dir" git-cleaner
install_dir="${GIT_CLEANER_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$install_dir"
install -m 0755 "$tmp_dir/git-cleaner" "$install_dir/git-cleaner"
printf 'Installed git-cleaner to %s/git-cleaner\n' "$install_dir"
