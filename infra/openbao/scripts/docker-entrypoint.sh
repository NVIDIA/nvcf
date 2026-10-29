#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu

# The chart invokes this path explicitly. Keep required plugin config here so
# replacing server.extraArgs cannot silently disable declarative registration.
case ${1:-} in
    server) shift; set -- server "$@" -config=/openbao/plugin-config ;;
    bao)
        if [ "${2:-}" = server ]; then
            shift 2
            set -- bao server "$@" -config=/openbao/plugin-config
        fi
        ;;
esac
exec "${OPENBAO_UPSTREAM_ENTRYPOINT:-/usr/local/bin/docker-entrypoint-upstream.sh}" "$@"
