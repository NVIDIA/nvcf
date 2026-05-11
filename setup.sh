#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# One-shot Bazelisk installer. After this, `bazel <cmd>` works and Bazelisk
# auto-downloads the version pinned in .bazelversion on first build.
set -euo pipefail

BAZELISK_VERSION="1.25.0"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

info()  { echo "[setup] $*"; }
error() { echo "[setup] ERROR: $*" >&2; }

check_command() { command -v "$1" >/dev/null 2>&1; }

install_bazelisk() {
  if check_command bazel; then
    local current
    current="$(bazel --version 2>/dev/null || true)"
    if echo "$current" | grep -qi bazelisk; then
      info "Bazelisk already installed: $current"
      return 0
    fi
  fi

  info "Installing Bazelisk ${BAZELISK_VERSION} to ${INSTALL_DIR}..."
  mkdir -p "$INSTALL_DIR"

  local os arch url
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64)  arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) error "Unsupported architecture: $arch"; exit 1 ;;
  esac

  url="https://github.com/bazelbuild/bazelisk/releases/download/v${BAZELISK_VERSION}/bazelisk-${os}-${arch}"
  info "Downloading from ${url}"

  if check_command curl; then
    curl -fSL -o "${INSTALL_DIR}/bazel" "$url"
  elif check_command wget; then
    wget -q -O "${INSTALL_DIR}/bazel" "$url"
  else
    error "Neither curl nor wget found"; exit 1
  fi

  chmod +x "${INSTALL_DIR}/bazel"
  info "Installed bazelisk as ${INSTALL_DIR}/bazel"
}

rc_file_for_shell() {
  local shell_name
  shell_name="$(basename "${SHELL:-}")"
  case "$shell_name" in
    zsh)  echo "$HOME/.zshrc" ;;
    bash)
      if [[ "$(uname -s)" == "Darwin" ]]; then
        echo "$HOME/.bash_profile"
      else
        echo "$HOME/.bashrc"
      fi
      ;;
    *) echo "" ;;
  esac
}

verify_path() {
  export PATH="${INSTALL_DIR}:$PATH"

  local marker="# added by nvcf setup.sh (Bazelisk PATH)"
  local line="export PATH=\"${INSTALL_DIR}:\$PATH\"  ${marker}"
  local rc_file
  rc_file="$(rc_file_for_shell)"

  if [[ -z "$rc_file" ]]; then
    info ""
    info "Could not detect your shell. Add this line to your shell profile:"
    info "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    info ""
    return 0
  fi

  if [[ -f "$rc_file" ]] && grep -qF "$marker" "$rc_file"; then
    info "PATH entry already present in $rc_file"
    return 0
  fi

  printf '\n%s\n' "$line" >> "$rc_file"
  info "Appended PATH entry to $rc_file"
  info "Open a new terminal or run: source $rc_file"
}

verify_install() {
  info "Verifying installation..."
  if ! check_command bazel; then
    error "bazel not found in PATH after install"
    exit 1
  fi
  bazel --version
  info ""
  info "Bazel will auto-download version $(cat "$(dirname "$0")/.bazelversion") on first build."
}

info "=== NVCF monorepo Bazel setup ==="
install_bazelisk
verify_path
verify_install
info ""
info "Ready! Try:"
info "  bazel build //src/clis/nvcf-cli/..."
info "  bazel build //src/libraries/go/lib/..."
