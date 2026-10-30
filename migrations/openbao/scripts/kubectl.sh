#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -eu

case "${NVCF_KUBECTL_MINOR-1.37}" in
  1.37) exec /usr/local/bin/kubectl-1.37 "$@" ;;
  1.36) exec /usr/local/bin/kubectl-1.36 "$@" ;;
  *)
    echo "NVCF_KUBECTL_MINOR must be 1.37 or 1.36" >&2
    exit 64
    ;;
esac
