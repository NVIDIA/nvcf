#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# Parse command line arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --preferred-cloud-backend=*)
      preferred_cloud_backend="${1#*=}"
      shift
      ;;
    --output-file=*)
      output_file="${1#*=}"
      shift
      ;;
    *)
      echo "Unknown parameter: $1"
      echo "Usage: $0 [--output_file=OUTPUT_FILE] [--preferred-cloud-backend=PREFERRED_CLOUD_BACKEND(horde|aws|none)]"
      exit 1
      ;;
  esac
done


if [[ $GITLAB_CI == "true" ]]; then
    OWNER="nvcf-byoo"
    INSTANCE_NAME_SUFFIX=${CI_JOB_ID}
else
    OWNER=$(whoami)
    INSTANCE_NAME_SUFFIX=$(date +%s)
fi

OUTPUT_FILE="${output_file:-instance_info.env}"
QUERY_TIMEOUT="${QUERY_TIMEOUT:-300}"
QUERY_INTERVAL="${QUERY_INTERVAL:-10}"


horde_cluster_id=santaclara_x86_cloudstack

retry_backoff() {

    local action=$1
    local max_attempts=$2
    local initial_delay=$3
    local attempt=0
    local delay=$initial_delay

    until $action; do
        attempt=$((attempt + 1))
        if [ $attempt -ge $max_attempts ]; then
            echo "Action '$action' failed after $max_attempts attempts."
            return 1
        fi
        echo "===== Action '$action' failed. Retrying ($attempt/$max_attempts) after $delay seconds... ====="
        sleep $delay
        delay=$((delay * 2))  # Exponential backoff: double the delay
    done
}



create_horde_instance() {

    response=$(curl -k -s -X 'GET' \
    "https://horde.nvidia.com:8443/api/v3/clouds/${horde_cluster_id}/capacity" \
    -H 'accept: application/json')

    echo "$response"

    # Check if the response is empty
    if [[ -z "$response" ]]; then
        echo "No capacity information found for cluster $horde_cluster_id"
        create_aws_instance
    fi

    # Extract the available capacity
    available_capacity=$(echo "$response" | jq '.gpu_models_for_ui["Gpu:NVIDIA L40"]')

    if [[ $available_capacity -lt 10 ]]; then
        echo "No available capacity for horde:$available_capacity"
        return 1
    fi



    export SSHPASS=$HORDE_PASSWORD
    
    json_data="{
        \"os\":\"Linux\",
        \"os_version\":\"Ubuntu 22.04\",
        \"arch\":\"x86_64\",
        \"memory\":16,
        \"cores\":8,
        \"gpu_count\":1,
        \"gpu_name\":\"NVIDIA L40\",
        \"owner\":\"${OWNER}\",
        \"instance_name\":\"${OWNER}-validator-${INSTANCE_NAME_SUFFIX}\",
        \"purpose\":\"development_environment\",
        \"lease_days\":1,
        \"storage\":250,
        \"template_name\": \"horde-desktop-ubuntu-2204-amd64-l40-9b0e70c7\",
        \"cluster_id\":\"${horde_cluster_id}\"
    }"

    response=$(curl -k -s -X POST https://horde.nvidia.com:8443/api/v3/images/clone-in-queue \
        -H "Content-Type: application/json" \
        -d "$json_data")

    echo "Create image success, response: $response"

    cluster_id=$(echo "$response" | jq -r '.form.cluster_id')
    instance_name=$(echo "$response" | jq -r '.form.instance_name')

    echo "Instance Name: $instance_name"

    status="not_running"

    elapsed=0
    request_status=""

    while [[ $elapsed -lt 120 ]]; do
        response=$(curl -k -s -X 'GET' \
            "https://horde.nvidia.com:8443/api/v3/tasks/?owner=${OWNER}&instance_name=${instance_name}" \
            -H 'accept: application/json')

        echo "$response"

        request_status=$(echo "$response" | jq -r '.[0].status')
        request_task_id=$(echo "$response" | jq -r '.[0].id')

        if [[ $request_status == "Pending" ]]; then
            sleep $QUERY_INTERVAL
            elapsed=$((elapsed + QUERY_INTERVAL))
            request_status="Pending"
        elif [[ $request_status == "Success" ]]; then
            request_status="Success"
            break
        else
            echo "check request_status error request_status: $request_status"
            break
        fi
    done

    if [[ $request_status == "Pending" || $request_status == "" ]]; then
        echo "Timeout reached: instance did not reach 'Success' status within 120 seconds."
        echo "Delete the pending task $request_task_id"
        response=$(curl -k -s -X 'DELETE' \
            "https://horde.nvidia.com:8443/api/v3/tasks/${request_task_id}" \
            -H 'accept: application/json')

        echo "$response"        
        return 1
    fi

    # https://horde.nvidia.com/api/v3/tasks/?owner=$&status=Pending
    elapsed=0

    while [[ $elapsed -lt $QUERY_TIMEOUT ]]; do
        # Curl the instance status
        response=$(curl -k -s -X 'GET' \
            "https://horde.nvidia.com:8443/api/v3/clouds/$cluster_id/instances?instance_name=$instance_name" \
            -H 'accept: application/json')
        
        echo "$response"

        # Check the JSON array length and status.status
        array_length=$(echo "$response" | jq 'length')
        status=$(echo "$response" | jq -r '.[0].status.status')

        if [[ $array_length -eq 1 && $status == "Running" ]]; then
            ip_address=$(echo "$response" | jq -r '.[0].resources[] | select(.res_type == "Network") | .address')
            instance_id=$(echo "$response" | jq -r '.[0].id')
            if sshpass -p "$SSHPASS" ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no horde@$ip_address "exit" 2>/dev/null; then
                echo "check instance is running"
                status="running"
                break
            else
                echo "check instance is running, but ssh failed"
            fi
        else
            echo "check instance in $cluster_id is not running, retrying in $QUERY_INTERVAL seconds..."
        fi

        # Wait for the specified interval
        sleep $QUERY_INTERVAL
        elapsed=$((elapsed + QUERY_INTERVAL))
    done

    # Check if timeout
    if [[ $elapsed -ge $QUERY_TIMEOUT ]]; then
        echo "Timeout reached: instance did not reach 'Running' status within $QUERY_TIMEOUT seconds."
        return 1
    fi

    export CLOUD_BACKEND=horde
    export INSTANCE_IP=$ip_address
    export INSTANCE_ID=$instance_id

    echo "CLOUD_BACKEND=${CLOUD_BACKEND}" > $OUTPUT_FILE
    echo "INSTANCE_IP=${INSTANCE_IP}" >> $OUTPUT_FILE
    echo "INSTANCE_ID=${INSTANCE_ID}" >> $OUTPUT_FILE

    echo "CLOUD_BACKEND=${CLOUD_BACKEND}"
    echo "INSTANCE_IP=${INSTANCE_IP}"
    echo "INSTANCE_ID=${INSTANCE_ID}"    
}


create_aws_instance() {

    if [[ -z "$AWS_ACCESS_KEY_ID" || -z "$AWS_SECRET_ACCESS_KEY" || -z "$AWS_SESSION_TOKEN" ]]; then
        echo "AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN is not set, please set it in the environment"
        exit 1
    fi

    IMAGE_ID="ami-034c3feaa4af88624" # Deep Learning Base OSS Nvidia Driver GPU AMI quickstart AMI, see https://docs.aws.amazon.com/dlami/latest/devguide/appendix-ami-release-notes.html
    INSTANCE_TYPE="g4dn.xlarge"
    KEY_NAME="byoo-test-key"
    SECURITY_GROUP_ID="sg-05c0dd046de4e29bd"
    SUBNET_ID="subnet-0e7c5f0b38ef5bb16"

    aws ec2 run-instances \
        --image-id $IMAGE_ID \
        --instance-type $INSTANCE_TYPE \
        --key-name $KEY_NAME \
        --security-group-ids $SECURITY_GROUP_ID \
        --subnet-id $SUBNET_ID \
        --block-device-mappings '[{"DeviceName":"/dev/sda1","Ebs":{"VolumeSize":75,"DeleteOnTermination":true}}]' \
        --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${OWNER}-validator-${INSTANCE_NAME_SUFFIX}}]" --output json > create_instance_response.json

    INSTANCE_IP=$(jq -r '.Instances[0].PrivateIpAddress' create_instance_response.json)
    INSTANCE_ID=$(jq -r '.Instances[0].InstanceId' create_instance_response.json)

    aws ec2 wait instance-running --instance-ids $INSTANCE_ID

    export CLOUD_BACKEND=aws
    export INSTANCE_IP=$INSTANCE_IP
    export INSTANCE_ID=$INSTANCE_ID

    echo "CLOUD_BACKEND=${CLOUD_BACKEND}" > $OUTPUT_FILE
    echo "INSTANCE_IP=${INSTANCE_IP}" >> $OUTPUT_FILE
    echo "INSTANCE_ID=${INSTANCE_ID}" >> $OUTPUT_FILE

    echo "CLOUD_BACKEND=${CLOUD_BACKEND}"
    echo "INSTANCE_IP=${INSTANCE_IP}"
    echo "INSTANCE_ID=${INSTANCE_ID}"
}


echo "Checking preferred cloud backend ${preferred_cloud_backend}..."
if [[ $preferred_cloud_backend == "horde" ]]; then
    create_horde_instance
 
elif [[ $preferred_cloud_backend == "aws" ]]; then
    create_aws_instance
else
    echo "Checking horde cluster capacity first..."
    retry_backoff create_horde_instance 4 10
    ret=$?
    if [[ $ret -ne 0 ]]; then
        echo "Failed to create horde instance, try to create aws instance"
        create_aws_instance
    fi
fi
