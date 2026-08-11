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

package service

// NOTE: This file contains V2 specific tests.
// It re-uses or re-defines mocks similar to service_test.go. Consider refactoring mocks into a shared test utility if complexity grows.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access" // Import for DBHandler interface
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
)

// --- Mocks (Duplicated/Adapted from service_test.go for V2) ---

// passDBHandlerV2 implements data_access.DBHandlerV2 for successful V2 test cases.
type passDBHandlerV2 struct{}

// V1 Method implementations Removed

// V2 Method implementations (relevant for V2 tests)
func (p *passDBHandlerV2) WriteDeploymentStageTransitionEvent(context.Context, types.DeploymentStageTransitionEvent) error {
	return nil
}
func (p *passDBHandlerV2) ListDeploymentStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	ste, _ := types.NewStageTransitionEvent("NCA_V2", uuid.New(), uuid.New(), uuid.New().String(), "test-v2", "test-v2", time.Now(), json.RawMessage(`{"test": "test-v2"}`))
	return []types.StageTransitionEvent{ste}, nil
}
func (p *passDBHandlerV2) ListDeploymentInstances(context.Context, uuid.UUID, uuid.UUID) ([]types.Instance, error) {
	return []types.Instance{{InstanceId: uuid.New().String(), LastEvent: "ready"}}, nil
}
func (p *passDBHandlerV2) GetDeploymentInstanceEvent(context.Context, uuid.UUID, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	ste, _ := types.NewStageTransitionEvent("NCA_V2", uuid.New(), uuid.New(), uuid.New().String(), "test-v2", "test-v2", time.Now(), json.RawMessage(`{"test": "test-v2"}`))
	return ste, nil
}
func (p *passDBHandlerV2) ArchiveDeploymentInstanceStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (p *passDBHandlerV2) ReadDeploymentDeploymentStats(context.Context, uuid.UUID, uuid.UUID) (types.DeploymentStats, error) {
	return types.DeploymentStats{FunctionVersionId: uuid.New(), Ready: 1}, nil
}

func (p *passDBHandlerV2) ListDeploymentInstancesPaginated(ctx context.Context, functionVersionId, deploymentId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	instances := []types.Instance{
		{InstanceId: uuid.New().String(), LastEvent: "ready"},
	}

	// Handle pagination parameters for testing
	limit := paginationParams.Limit
	if limit <= 0 || limit > len(instances) {
		limit = len(instances)
	}

	// Return instances up to the limit
	returnedInstances := instances[:limit]
	hasMore := limit < len(instances)

	return data_access.PaginatedInstancesResponse{
		Instances: returnedInstances,
		Pagination: data_access.PaginationMeta{
			PageSize:      len(returnedInstances), // ✅ Dynamic calculation
			NextPageToken: "",                     // Simple test case
			HasMore:       hasMore,                // ✅ Dynamic calculation
		},
	}, nil
}

func (p *passDBHandlerV2) UpsertEventV3(ctx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	return nil
}

func (p *passDBHandlerV2) UpsertStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return nil
}

func (p *passDBHandlerV2) UpsertFilteredStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return nil
}

func (p *passDBHandlerV2) BulkUpsertEventsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	return nil
}

func (p *passDBHandlerV2) BulkUpsertStatsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	return nil
}

func (p *passDBHandlerV2) GetStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return []data_access.StatsV3Record{}, nil
}

func (p *passDBHandlerV2) GetFilteredStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return []data_access.StatsV3Record{}, nil
}

func (p *passDBHandlerV2) GetEventsV3(ctx context.Context, namespace, eventContext string) ([]data_access.EventV3Record, error) {
	return []data_access.EventV3Record{}, nil
}

func (p *passDBHandlerV2) Close() error { return nil }

// Interface assertion for V2
var _ data_access.DBHandlerV2 = (*passDBHandlerV2)(nil)

// PassFixtureV2 provides a test fixture with the successful mock DB handler.
type PassFixtureV2 struct{}

// InitConns now returns only the DBHandlerV2 interface.
func (f *PassFixtureV2) InitConns() (data_access.DBHandlerV2, error) {
	return &passDBHandlerV2{}, nil
}

// failDBHandlerV2 implements DBHandlerV2 returning errors for V2 methods.
// No longer embeds passDBHandlerV2 as V1 methods are removed.
type failDBHandlerV2 struct{}

// V1 Methods Removed

func (p *failDBHandlerV2) WriteDeploymentStageTransitionEvent(context.Context, types.DeploymentStageTransitionEvent) error {
	return fmt.Errorf("v2 write fail")
}
func (p *failDBHandlerV2) ListDeploymentStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	return nil, fmt.Errorf("v2 list events fail")
}
func (p *failDBHandlerV2) ListDeploymentInstances(context.Context, uuid.UUID, uuid.UUID) ([]types.Instance, error) {
	return nil, fmt.Errorf("v2 list instances fail")
}
func (p *failDBHandlerV2) GetDeploymentInstanceEvent(context.Context, uuid.UUID, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.ErrStageTransitionEvent, fmt.Errorf("v2 get event fail")
}
func (p *failDBHandlerV2) ArchiveDeploymentInstanceStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) error {
	return fmt.Errorf("v2 archive fail")
}
func (p *failDBHandlerV2) ReadDeploymentDeploymentStats(context.Context, uuid.UUID, uuid.UUID) (types.DeploymentStats, error) {
	return types.ErrDeploymentStats, fmt.Errorf("v2 read stats fail")
}

func (p *failDBHandlerV2) ListDeploymentInstancesPaginated(ctx context.Context, functionVersionId, deploymentId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, fmt.Errorf("v2 paginated list fail")
}

func (p *failDBHandlerV2) UpsertEventV3(ctx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	return fmt.Errorf("v2 upsert event fail")
}

func (p *failDBHandlerV2) UpsertStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return fmt.Errorf("v2 upsert stats fail")
}

func (p *failDBHandlerV2) UpsertFilteredStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return fmt.Errorf("v2 upsert filtered stats fail")
}

func (p *failDBHandlerV2) BulkUpsertEventsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	return fmt.Errorf("v2 bulk upsert events fail")
}

func (p *failDBHandlerV2) BulkUpsertStatsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	return fmt.Errorf("v2 bulk upsert stats fail")
}

func (p *failDBHandlerV2) GetStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return nil, fmt.Errorf("v2 get stats fail")
}

func (p *failDBHandlerV2) GetFilteredStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return nil, fmt.Errorf("v2 get filtered stats fail")
}

func (p *failDBHandlerV2) GetEventsV3(ctx context.Context, namespace, eventContext string) ([]data_access.EventV3Record, error) {
	return nil, fmt.Errorf("v2 get events fail")
}

func (p *failDBHandlerV2) Close() error { return fmt.Errorf("v2 close fail") } // Add Close method

// Interface assertion for V2
var _ data_access.DBHandlerV2 = (*failDBHandlerV2)(nil)

// FailFixtureV2 provides a test fixture with the failing mock DB handler.
type FailFixtureV2 struct{}

// InitConns now returns only the DBHandlerV2 interface.
func (f *FailFixtureV2) InitConns() (data_access.DBHandlerV2, error) {
	return &failDBHandlerV2{}, nil
}

// --- V2 Tests ---

func TestPostDeploymentStageTransitionEventV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()
	testInstanceId := uuid.New().String()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		postData   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 SuccessfulPost",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusAccepted,
			wantBody:   "",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
			postData: fmt.Sprintf(`{
			  "ncaId": "NCA_V2_POST",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c7",
			  "functionVersionId": "%s",
			  "deploymentId": "%s",
			  "instanceId": "%s",
			  "event": "building",
			  "eventType": "test_post_v2",
			  "timestamp": "2025-01-10T10:00:00Z",
			  "details": {"source": "test-v2"}
			}`, testFnVerId, testDeployId, testInstanceId), // Note: Body deploymentId might be ignored by handler
		},
		{
			name:       "V2 Bad event",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "event is not valid",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
			postData: fmt.Sprintf(`{
				"ncaId": "NCA_V2_POST", "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c7",
				"functionVersionId": "%s", "deploymentId": "%s", "instanceId": "%s",
				"event": "invalid-event-name", "eventType": "test_post_v2", "timestamp": "2025-01-10T10:00:00Z", "details": {"source": "test-v2"}
			  }`, testFnVerId, testDeployId, testInstanceId),
		},
		{
			name:       "V2 Missing Required Fields",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "event has missing required values", // Updated expected error based on V2 validation
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
			postData: fmt.Sprintf(`{
				"ncaId": "NCA_V2_POST", "functionVersionId": "%s", "deploymentId": "%s", "instanceId": "%s",
				"event": "building", "eventType": "test_post_v2", "timestamp": "2025-01-10T10:00:00Z", "details": {"source": "test-v2"}
				 }`, testFnVerId, testDeployId, testInstanceId), // Missing functionId
		},
		{
			name:       "V2 Invalid DeploymentId in URL",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid", // Invalid UUID
				"instanceId":        testInstanceId,
			},
			postData: fmt.Sprintf(`{
				"ncaId": "NCA_V2_POST", "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c7",
				"functionVersionId": "%s", "deploymentId": "%s", "instanceId": "%s",
				"event": "building", "eventType": "test_post_v2", "timestamp": "2025-01-10T10:00:00Z", "details": {"source": "test-v2"}
			  }`, testFnVerId, testDeployId, testInstanceId),
		},
		{
			name:       "V2 DB Write Failure",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
			postData: fmt.Sprintf(`{
				"ncaId": "NCA_V2_FAIL", "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c8",
				"functionVersionId": "%s", "deploymentId": "%s", "instanceId": "%s",
				"event": "building", "eventType": "test_post_v2_fail", "timestamp": "2025-01-10T10:01:00Z", "details": {"source": "test-fail"}
			  }`, testFnVerId, testDeployId, testInstanceId),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// --- Test Setup ---
			dbHandlerV2, err := tt.fixture.InitConns() // Get V2 handler from fixture
			require.NoError(t, err, "Fixture InitConns failed")

			// Manually create Connections struct for V2 test
			connsForTest := Connections{DbHandlerV2: dbHandlerV2}

			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()

			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			// --- Request ---
			reader := strings.NewReader(tt.postData)
			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/%s",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"],
				tt.urlVars["instanceId"])

			req := NewRequestWithLogger("POST", url, reader, logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			// --- Server Call ---
			server.PostDeploymentStageTransitionEvent(rr, req)

			// --- Assertions ---
			assert.Equal(t, tt.wantStatus, rr.Code, "HTTP status code mismatch")
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody), "Response body mismatch, got: %s, want contains: %s", rr.Body.String(), tt.wantBody) // Use assert.True
			}

			// Verify events are published when successful
			if tt.wantStatus == http.StatusAccepted {
				eventsV2 := publisher.GetEventsV2()
				assert.Equal(t, 1, len(eventsV2), "Expected one V2 event to be published")
				if len(eventsV2) > 0 {
					assert.Equal(t, tt.urlVars["deploymentId"], eventsV2[0].DeploymentId.String(), "DeploymentId mismatch")
				}
			}
		})
	}
}

func TestListDeploymentStageTransitionEventsV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()
	testInstanceId := uuid.New().String()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 Get Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			wantBody:   "test-v2", // From mock data
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad InstanceId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "failed to parse instance id",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        "invalid-instance-id-format",
			},
		},
		{
			name:       "V2 Get Fail Not Found",        // Assuming fail handler returns specific not found
			fixture:    &FailFixtureV2{},               // Using FailFixtureV2 which returns generic error, adjust if specific needed
			wantStatus: http.StatusInternalServerError, // Or StatusNotFound if mock returns that
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/%s",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"],
				tt.urlVars["instanceId"])

			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.ListDeploymentStageTransitionEvents(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
		})
	}
}

func TestListDeploymentInstancesV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus  int
		wantAmount  int // Expected number of instances in the response
		queryParams string
		wantBody    string
		urlVars     map[string]string
	}{
		{
			name:       "V2 List Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			wantAmount: 1, // Based on passDBHandlerV2 mock
			wantBody:   "instanceId",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:        "V2 List Filter Success", // Filter logic reused from V1
			fixture:     &PassFixtureV2{},
			wantStatus:  http.StatusOK,
			wantAmount:  1, // Mock returns 1 instance with 'ready', filter matches
			queryParams: "?eventFilter=ready",
			wantBody:    "instanceId",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:        "V2 List Filter No Match",
			fixture:     &PassFixtureV2{},
			wantStatus:  http.StatusOK,
			wantAmount:  0, // Mock returns 1 'ready', filter doesn't match
			queryParams: "?eventFilter=pending",
			wantBody:    "[]", // Expect empty JSON array
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantAmount: 0,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantAmount: 0,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
			},
		},
		{
			name:       "V2 DB Fail",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantAmount: 0,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances%s",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"],
				tt.queryParams)

			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.ListDeploymentInstances(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
			if tt.wantStatus == http.StatusOK {
				var results []types.Instance
				err := json.Unmarshal(rr.Body.Bytes(), &results)
				assert.NoError(t, err)
				assert.Equal(t, tt.wantAmount, len(results))
			}
		})
	}
}

func TestArchiveDeploymentInstanceStageTransitionEventsV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()
	testInstanceId := uuid.New().String()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 Archive Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
				"instanceId":        testInstanceId,
			},
		},
		{
			name:       "V2 Bad InstanceId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'instanceId' is not valid",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        "invalid-instance",
			},
		},
		{
			name:       "V2 Archive Fail",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/%s",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"],
				tt.urlVars["instanceId"])

			req := NewRequestWithLogger("DELETE", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.ArchiveDeploymentInstanceStageTransitionEvents(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
		})
	}
}

func TestArchiveDeploymentFunctionVersionStageTransitionEventsV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 Archive All Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
			},
		},
		{
			name:       "V2 List Instances Fail",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		// Note: Add a test case using a mock that fails on the ArchiveDeploymentInstanceStageTransitionEvents call
		// inside the loop, potentially using a custom mock or modifying FailFixtureV2.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"])

			req := NewRequestWithLogger("DELETE", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.ArchiveDeploymentFunctionVersionStageTransitionEvents(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
		})
	}
}

func TestGetDeploymentDeploymentStatsV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 Get Stats Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			wantBody:   "\"ready\":1", // Check for a specific stat from the mock
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
			},
		},
		{
			name:       "V2 DB Fail",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
			},
		},
		// Add test for 'not found' if FailFixtureV2 is adjusted or new fixture added
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/stats",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"])

			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.GetDeploymentDeploymentStats(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
		})
	}
}

func TestGetDeploymentInstanceEventV2(t *testing.T) {
	testFnVerId := uuid.New()
	testDeployId := uuid.New()
	testInstanceId := uuid.New().String()

	tests := []struct {
		name    string
		fixture interface {
			InitConns() (data_access.DBHandlerV2, error) // Updated fixture signature
		}
		wantStatus int
		wantBody   string
		urlVars    map[string]string
	}{
		{
			name:       "V2 Get Event Success",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusOK,
			wantBody:   "test-v2",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
				"event":             "building", // Example valid event
			},
		},
		{
			name:       "V2 Bad FnVerId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'functionVersionId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": "invalid-uuid",
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
				"event":             "building",
			},
		},
		{
			name:       "V2 Bad DeploymentId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'deploymentId' field is not valid UUID",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      "invalid-uuid",
				"instanceId":        testInstanceId,
				"event":             "building",
			},
		},
		{
			name:       "V2 Bad InstanceId",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'instanceId' field is not valid",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        "invalid-instance",
				"event":             "building",
			},
		},
		{
			name:       "V2 Bad Event Name",
			fixture:    &PassFixtureV2{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'event' field is invalid",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
				"event":             "invalid?event=name",
			},
		},
		{
			name:       "V2 DB Fail",
			fixture:    &FailFixtureV2{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error",
			urlVars: map[string]string{
				"functionVersionId": testFnVerId.String(),
				"deploymentId":      testDeployId.String(),
				"instanceId":        testInstanceId,
				"event":             "building",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbHandlerV2, err := tt.fixture.InitConns()
			require.NoError(t, err)
			connsForTest := Connections{DbHandlerV2: dbHandlerV2} // Manually create Connections
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			server := NewServer(connsForTest, logger, publisher, version, &config.HTTPClientConfig{}, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/%s/events/%s",
				tt.urlVars["functionVersionId"],
				tt.urlVars["deploymentId"],
				tt.urlVars["instanceId"],
				tt.urlVars["event"])

			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			req = mux.SetURLVars(req, tt.urlVars)
			rr := httptest.NewRecorder()

			server.GetDeploymentInstanceEvent(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
			if tt.wantBody != "" {
				assert.True(t, strings.Contains(rr.Body.String(), tt.wantBody))
			}
		})
	}
}
