#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# Usage
namespace="${1:-cassandra-system}"
statefulset="${2:-cassandra}"

# Environment variables must be set
if [ -z "$CASSANDRA_USER" ] || [ -z "$CASSANDRA_PASSWORD" ]; then
  echo "Error: CASSANDRA_USER and CASSANDRA_PASSWORD environment variables must be set"
  exit 1
fi

# How many desired replicas

initialize_db() {
  # Define the end variable for a 10-minute timeout
  local end=$((SECONDS + 600))

  local desired_replicas=$(kubectl get statefulset ${statefulset} -n ${namespace} -o jsonpath='{.spec.replicas}')
  if [[ "$desired_replicas" -eq 0 ]]; then
    echo "StatefulSet may not be configured correctly, 0 desired replicas is not valid. Exiting."
    exit 1
  fi

  for i in $(seq 0 $((desired_replicas - 1))); do
    while [[ $(kubectl get pod $statefulset-${i} -n ${namespace} -o jsonpath='{.status.containerStatuses[0].ready}') != "true" ]]; do
      if [ $SECONDS -gt $end ]; then
        echo "Timeout waiting for pod ${statefulset}-${i} to be ready"
        return 1
      fi
      echo "Waiting for pod ${statefulset}-${i} to be ready..."
      sleep 5
    done
  done
  echo "All Cassandra pods are ready"

  echo "Initializing Cassandra cluster with keyspace.cql"
  #
  # A ConfigMap containing the cql script is mounted to the cassandra container
  # The ConfigMap is created as a Hook (see templates) and the Bitnami chart
  # provides values to specify arbitrary extra volumes/mounts.
  #
  # Always select the 0th pod
  if ! kubectl exec ${statefulset}-0 -c cassandra -n ${namespace} -- \
    cqlsh -u "${CASSANDRA_USER}" -p "${CASSANDRA_PASSWORD}" localhost -f /opt/nvcf/cassandra/cql/keyspace.cql; then
    echo "Failed to successfully execute CQL"
    return 1
  fi
}

if ! initialize_db ${namespace} ${statefulset}; then
  exit 1
else
  echo "Successfully initialized the db"
fi
