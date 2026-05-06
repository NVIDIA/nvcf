#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


instance_id=$1
cloud_backend=$2

echo "teardown instance $instance_id in $cloud_backend"

QUERY_TIMEOUT="${QUERY_TIMEOUT:-300}"
QUERY_INTERVAL="${QUERY_INTERVAL:-10}"

if [[ $cloud_backend == "horde" ]]; then
    cluster_id=santaclara_x86_cloudstack
    response=$(curl -k -s -X 'POST' \
        "https://horde.nvidia.com/api/v3/instances/${instance_id}/${cluster_id}/stop")
    echo "Stop instance response: $response"

    elapsed=0

    while [[ $elapsed -lt $QUERY_TIMEOUT ]]; do
        response=$(curl -s -k -X 'GET' \
            "https://horde.nvidia.com:8443/api/v3/instances/${instance_id}/${cluster_id}/status" \
            -H 'accept: application/json')
        status=$(echo "$response" | jq -r '.status')
        if [[ $status == "Stopped" ]]; then
            break
        fi
        echo "Get instance status response: $response, waiting for instance to stop..."
        sleep $QUERY_INTERVAL
        elapsed=$((elapsed + QUERY_INTERVAL))
    done


    response=$(curl -s -k -X 'POST' \
        "https://horde.nvidia.com/api/v3/instances/${instance_id}/${cluster_id}/destroy")
    echo "Destroy instance response: $response"

elif [[ $cloud_backend == "aws" ]]; then
    aws ec2 terminate-instances --instance-ids $instance_id
fi


