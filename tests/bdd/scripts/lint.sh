#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Run golangci-lint against tests/bdd. tests/bdd carries its own go.mod
# so golangci-lint must be invoked from inside that directory; running
# it from the repo root produces a "no go files to analyze" or a
# "directory prefix does not contain main module" error that is
# confusing for reviewers who run the AGENTS.md command from a wrong
# cwd.
#
# This wrapper cds into tests/bdd before invoking the linter so it
# works from any cwd in the repo. Forwarded args (e.g. --fast) pass
# through to golangci-lint after the canonical config flag.
#
# Usage:
#   tests/bdd/scripts/lint.sh [extra golangci-lint args]
#
# Example:
#   ./tests/bdd/scripts/lint.sh                 # full run
#   ./tests/bdd/scripts/lint.sh --fix           # auto-fix where supported

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
bdd_dir="$(cd "$script_dir/.." && pwd -P)"

cd "$bdd_dir"
exec golangci-lint run --config .golangci.yml "$@" ./...
