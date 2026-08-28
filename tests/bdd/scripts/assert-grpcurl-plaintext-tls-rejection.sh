#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Normalize the grpcurl 1.9.3 client-side diagnostic observed when plaintext
# HTTP/2 is sent to a healthy TLS listener. Unexpected command, proto, usage,
# binary, and endpoint failures remain errors instead of satisfying the test.

set -euo pipefail

diagnostic="$(cat)"
case "${diagnostic}" in
  *"context deadline exceeded"*)
    printf '%s\n' 'plaintext-watch-rejected=tls-listener-timeout'
    ;;
  *)
    printf '%s\n' 'plaintext Watch failed without the expected TLS-listener transport timeout' >&2
    exit 1
    ;;
esac
