#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0
#
# Not a contribution
# Changes made by NVIDIA CORPORATION & AFFILIATES enabling ESS agent rebrand and packaging or otherwise documented as
# NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
# <NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>

# Don't use dumb-init as it isn't required and the end-user has the option
# to set it via the `--init` option.

set -e

# If the user is trying to run ess-agent directly with some arguments,
# then pass them to ess-agent.
# On alpine /bin/sh is busybox which supports this bashism.
if [ "${1:0:1}" = '-' ]
then
    set -- /bin/ess-agent "$@"
fi

# MUST exec here for consul-template to replace the shell as PID 1 in order
# to properly propagate signals from the OS to the consul-template process.
exec "$@"
