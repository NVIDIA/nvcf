#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

set -euo pipefail

# Build and push the webhook Docker image

REGISTRY="${REGISTRY:-nvcr.io/nvidia}"
IMAGE_NAME="${IMAGE_NAME:-nvcf-pod-mutator}"
TAG="${TAG:-latest}"
FULL_IMAGE="$REGISTRY/$IMAGE_NAME:$TAG"

echo "🐳 Building webhook Docker image"
echo "   Image: $FULL_IMAGE"
echo ""

# Build the image
echo "📦 Building Docker image..."
docker build -t "$FULL_IMAGE" .

echo "✅ Build complete!"
echo ""
echo "🚀 To push the image, run:"
echo "   docker push $FULL_IMAGE"
echo ""
echo "📝 Update deploy/webhook-deployment.yaml with:"
echo "   image: $FULL_IMAGE"


