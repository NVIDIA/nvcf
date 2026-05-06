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

package fnds

import (
	"context"

	nvcffndstypes "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
	"github.com/google/uuid"
)

type Client interface {
	GetNcaId() string
	CreateEvent(ctx context.Context, eventData nvcffndstypes.StageTransitionEvent) error
	NewStageTransitionEvent(ncaId string, functionId uuid.UUID, functionVersionId uuid.UUID, instanceId string, event string, eventType string, detailsJSON []byte) (nvcffndstypes.StageTransitionEvent, error)
}

// fakeFndsClient is a no-op implementation of functionDeploymentServiceClient
type fakeFndsClient struct {
	NcaID string
}

// NewFakeFndsClient creates a new fakeFndsClient with the specified NCA ID
func NewFakeClient(ncaId string) *fakeFndsClient {
	return &fakeFndsClient{
		NcaID: ncaId,
	}
}

// GetNcaId returns the NCA ID
func (a fakeFndsClient) GetNcaId() string {
	return a.NcaID
}

// CreateEvent is a no-op implementation that always succeeds silently
func (a fakeFndsClient) CreateEvent(ctx context.Context, eventData nvcffndstypes.StageTransitionEvent) error {
	// Simply return nil to indicate success
	return nil
}

func (a fakeFndsClient) NewStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	detailsJSON []byte,
) (nvcffndstypes.StageTransitionEvent, error) {
	return nvcffndstypes.StageTransitionEvent{}, nil
}
