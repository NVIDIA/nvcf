#!/usr/bin/env bash
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

set -uo pipefail

: "${ORG:?set ORG to your NGC org ID}"

TARGETED_DEPLOYMENT_SPECIFICATION=GB200:AWS.GPU.GB200_4x.x2:nvcf-dgxc-k8s-aws-use1-prd8
CONFIGURATION_FILE=gb200-override.yaml
CHART_NAME=multi-node-task-test
CHART_VERSION=0.1.0

# ngc registry chart create $ORG/$CHART_NAME --short-desc "Multi-node task test"

ngc registry chart remove $ORG/$CHART_NAME:$CHART_VERSION -y
rm $CHART_NAME-$CHART_VERSION.tgz

set -e

helm package multi-node-task-test/

ngc registry chart push $ORG/$CHART_NAME:$CHART_VERSION

ngc cf task create \
  --org $ORG \
  --name $CHART_NAME \
  --gpu-specification $TARGETED_DEPLOYMENT_SPECIFICATION \
  --configuration-file $CONFIGURATION_FILE \
  --max-runtime-duration 1H \
  --max-queued-duration 1H \
  --termination-grace-period-duration 1H \
  --result-handling-strategy NONE \
  --helm-chart $ORG/$CHART_NAME:$CHART_VERSION
