# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# /usr/bin/bash

# check that the correct env vars are set
ENV_VARS=(
    "NODES_PER_INSTANCE"
    "POD_NAME"
    "HEADLESS_SERVICE_NAME");
for var in "${ENV_VARS[@]}"; do
    if [ -z "${!var}" ]; then
        echo "Error: $var is not set."
        exit 1
    fi
done

set -xe

export HOSTFILE="/hosts";
export REPLICA=$(echo $POD_NAME | awk -F'-' '{print $NF}');
export REPLICA_GROUP=$(echo $POD_NAME | rev | cut -d'-' -f2- | rev);
export NAMESPACE=$(cat /var/run/secrets/kubernetes.io/serviceaccount/namespace)

# Dump the environment to load when the root user logs into SSH
mkdir -p "$HOME/.ssh"
env | grep -i -E '^NV|^LD_|^CUDA|^LIBRARY' > "$HOME/.ssh/environment"
chmod 600 "$HOME/.ssh/environment"
# Hard-code path environment variable
sed -i '1iexport PATH=/usr/local/nvidia/bin:/usr/local/cuda/bin:$PATH' "$HOME/.bashrc"
# Suppress the login message
touch "$HOME/.hushlogin"

# if not headnode
if [[ $REPLICA != 0 ]]; then
  # Start sshd server
  echo "Starting sshd server"
  /usr/sbin/sshd -D &

  # spin up health server
  uvicorn health_only_server:app --host=0.0.0.0
else # else (this is head node)
    for ((i=0; i<NODES_PER_INSTANCE; i++))
    do
        pod_hostname="${REPLICA_GROUP}-$i";

        # build $HOSTFILE
        echo $pod_hostname >> ${HOSTFILE};
        if [[ $i == 0 ]]; then
            continue;
        fi;
        # ensure each replica is active
        echo "Waiting for replica $i to start";
        while ! ssh_helper "${pod_hostname}" hostname ;
        do
            echo "pod $pod_hostname not ready yet, sleeping for 10 seconds";
            sleep 1;
        done;
        echo "pod $pod_hostname is ready!"
    done;

# run main app
uvicorn server:app --host=0.0.0.0
fi

