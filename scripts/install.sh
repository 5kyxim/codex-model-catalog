#!/bin/sh
#
# One-command installer for Codex Model Launcher.
#
# Downloads the latest darwin/arm64 release binary, verifies its SHA-256
# checksum, installs it into ~/.codex/bin, then builds the app bundle.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/5kyxim/codex-model-catalog/main/scripts/install.sh | sh
#
# Env overrides (advanced):
#   CODEX_MODEL_CATALOG_BASE_URL  base URL of release assets (default: GitHub latest/download)
#   CODEX_MODEL_CATALOG_APP_DIR   where to create the .app (passed through)
#   CODEX_MODEL_CATALOG_WRAPPER   wrapper binary path (passed through)
set -eu

repo="5kyxim/codex-model-catalog"
asset="codex-model-catalog_darwin_arm64.tar.gz"
base_url="${CODEX_MODEL_CATALOG_BASE_URL:-https://github.com/${repo}/releases/latest/download}"

if [ "$(uname -s)" != "Darwin" ]; then
  printf '%s\n' "error: Codex Model Launcher 目前只支持 macOS" >&2
  exit 1
fi

if [ "$(uname -m)" != "arm64" ]; then
  printf '%s\n' "error: 当前只发布 Apple Silicon (arm64) 版本" >&2
  exit 1
fi

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/codex-model-launcher.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

cd "$tmpdir"

printf '%s\n' "Downloading latest release assets..."
curl -fsSL "${base_url}/${asset}" -o "$asset"
curl -fsSL "${base_url}/checksums.txt" -o checksums.txt

printf '%s\n' "Verifying SHA-256 checksum..."
shasum -a 256 -c checksums.txt

tar -xzf "$asset"
version="$(cat VERSION 2>/dev/null || printf '%s' 'unknown')"

mkdir -p "$HOME/.codex/bin"
install -m 0755 codex-model-catalog "$HOME/.codex/bin/codex-model-catalog"

CODEX_MODEL_CATALOG_VERSION="$version" ./scripts/install-macos-app.sh

printf '%s\n' "Installed Codex Model Launcher ${version}"
printf '%s\n' "1. 按 Command-Q 完全退出正在运行的 Codex"
printf '%s\n' "2. 打开 $HOME/Applications/Codex Model Launcher.app"
