/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package interfaces

import (
	"context"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

// EventPublisher defines the interface for publishing single events.
type EventPublisher interface {
	Publish(ctx context.Context, event types.StageTransitionEvent)
	PublishV2(ctx context.Context, event types.DeploymentStageTransitionEvent)
}

// BatchStorageClient interface for clients saving events into storage systems
type BatchStorageClient interface {
	StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) error
	StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error
}
