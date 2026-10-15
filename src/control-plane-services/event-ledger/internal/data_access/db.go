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

package data_access

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/google/uuid"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
)

// PaginationParams defines parameters for paginated requests
type PaginationParams struct {
	Limit     int    `json:"limit"`
	PageToken string `json:"pageToken,omitempty"`
}

// PaginationMeta contains pagination metadata for responses
type PaginationMeta struct {
	PageSize      int    `json:"pageSize"`
	NextPageToken string `json:"nextPageToken,omitempty"`
	HasMore       bool   `json:"hasMore"`
}

// PaginatedInstancesResponse represents a paginated response for instances
type PaginatedInstancesResponse struct {
	Instances  []types.Instance `json:"instances"`
	Pagination PaginationMeta   `json:"pagination"`
}

// NewPaginationParams creates pagination params with defaults
func NewPaginationParams(limit int, pageToken string) PaginationParams {
	if limit <= 0 || limit > 100 {
		limit = 100 // Default limit, max 100
	}
	return PaginationParams{
		Limit:     limit,
		PageToken: pageToken,
	}
}

// ApplyDefaultLimit applies default page size if limit is 0
func (p PaginationParams) ApplyDefaultLimit(defaultLimit int) PaginationParams {
	if p.Limit == 0 {
		p.Limit = defaultLimit
	}
	return p
}

type DBHandler interface {
	// V1 Methods
	CheckStatus(ctx context.Context) map[string]error
	WriteStageTransitionEvent(traceCtx context.Context, msg types.StageTransitionEvent) error
	ListStageTransitionEvents(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string) ([]types.StageTransitionEvent, error)
	ListInstances(traceCtx context.Context, functionVersionId uuid.UUID) ([]types.Instance, error)
	// Paginated version of ListInstances
	ListInstancesPaginated(traceCtx context.Context, functionVersionId uuid.UUID, paginationParams PaginationParams) (PaginatedInstancesResponse, error)
	GetInstanceEvent(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string, event codex.Event) (types.StageTransitionEvent, error)
	ArchiveInstanceStageTransitionEvents(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string) error
	ReadDeploymentStats(traceCtx context.Context, functionVersionId uuid.UUID) (types.DeploymentStats, error)

	Close() error
}

// DBHandlerV2 defines the interface for V2 database operations (Deployment ID aware).
type DBHandlerV2 interface {
	// V2 Methods (Deployment ID aware)
	WriteDeploymentStageTransitionEvent(traceCtx context.Context, msg types.DeploymentStageTransitionEvent) error
	ListDeploymentStageTransitionEvents(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string) ([]types.StageTransitionEvent, error)
	ListDeploymentInstances(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID) ([]types.Instance, error)
	// Paginated version of ListDeploymentInstances
	ListDeploymentInstancesPaginated(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, paginationParams PaginationParams) (PaginatedInstancesResponse, error)
	GetDeploymentInstanceEvent(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string, event codex.Event) (types.StageTransitionEvent, error)
	ArchiveDeploymentInstanceStageTransitionEvents(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string) error
	ReadDeploymentDeploymentStats(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID) (types.DeploymentStats, error)
	// V3 Methods (events)
	UpsertEventV3(traceCtx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error
	UpsertStatsV3(traceCtx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error
	UpsertFilteredStatsV3(traceCtx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error
	BulkUpsertEventsV3(traceCtx context.Context, events []EventV3UpsertRecord) error
	BulkUpsertStatsV3(traceCtx context.Context, events []EventV3UpsertRecord) error
	GetStatsV3(traceCtx context.Context, namespace string) ([]StatsV3Record, error)
	GetFilteredStatsV3(traceCtx context.Context, namespace string) ([]StatsV3Record, error)
	GetEventsV3(traceCtx context.Context, namespace, eventContext string) ([]EventV3Record, error)
	Close() error // Include Close in V2 as well, assuming the underlying connection needs closing.
}

// StatsV3Record represents a single row from the stats_v3 table
type StatsV3Record struct {
	Context   string
	EventName string
	Timestamp time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventV3Record represents a single row from the events_v3 table
type EventV3Record struct {
	EventName string
	Source    string
	Details   json.RawMessage
	Timestamp time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventV3UpsertRecord holds all fields needed to write a row to events_v3 or stats_v3.
// Unlike EventV3Record (read path), this includes the partition and clustering key columns.
type EventV3UpsertRecord struct {
	Namespace string
	Context   string
	EventName string
	Source    string
	Details   json.RawMessage
	Timestamp time.Time
}

// CloudEventsResilienceHandler defines the interface for CloudEvents fallback and retry operations
type CloudEventsResilienceHandler interface {
	// Failed event operations
	StoreFailedCloudEvent(ctx context.Context, eventType string, eventData []byte) error
	StreamFailedCloudEvents(ctx context.Context, limit int) iter.Seq[FailedCloudEvent]
	UpdateFailedCloudEventRetry(ctx context.Context, id uuid.UUID, retryCount int) error
	DeleteFailedCloudEvent(ctx context.Context, id uuid.UUID) error

	// Leader election for retry coordination
	TryBecomeLeader(ctx context.Context, instanceID string, leaseDuration time.Duration) (bool, error)
}

// FailedCloudEvent represents a CloudEvent that failed to be sent
type FailedCloudEvent struct {
	ID                 uuid.UUID `json:"id"`
	EventType          string    `json:"event_type"` // "v1" or "v2"
	EventData          string    `json:"event_data"` // JSON serialized event
	RetryCount         int       `json:"retry_count"`
	LastRetryTimestamp time.Time `json:"last_retry_timestamp"`
	CreatedAt          time.Time `json:"created_at"`
}
