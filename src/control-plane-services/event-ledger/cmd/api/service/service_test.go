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

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap/zaptest"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

// Pass Fixtures ====================================================================
type passDBHandler struct{}

func (p *passDBHandler) CheckStatus(context.Context) map[string]error {
	return map[string]error{
		"db": nil,
	}
}

func (p *passDBHandler) WriteStageTransitionEvent(context.Context, types.StageTransitionEvent) error {
	return nil
}

func (p *passDBHandler) ListStageTransitionEvents(context.Context, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	ste, _ := types.NewStageTransitionEvent(
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		uuid.New(),
		uuid.New(),
		uuid.New().String(),
		"test",
		"test",
		time.Now(),
		json.RawMessage(`{"test": "test"}`))
	return []types.StageTransitionEvent{ste}, nil
}

func (p *passDBHandler) ListInstances(context.Context, uuid.UUID) ([]types.Instance, error) {
	return []types.Instance{
		{InstanceId: uuid.New().String(), LastEvent: "pending"},
		{InstanceId: uuid.New().String(), LastEvent: "downloadingModel"},
		{InstanceId: uuid.New().String(), LastEvent: "ready"},
	}, nil
}

func (p *passDBHandler) ListInstancesPaginated(ctx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	instances := []types.Instance{
		{InstanceId: uuid.New().String(), LastEvent: "pending"},
		{InstanceId: uuid.New().String(), LastEvent: "downloadingModel"},
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

func (p *passDBHandler) ArchiveInstanceStageTransitionEvents(context.Context, uuid.UUID, string) error {
	return nil
}

func (p *passDBHandler) ReadDeploymentStats(context.Context, uuid.UUID) (types.DeploymentStats, error) {
	return types.DeploymentStats{
		FunctionVersionId:          uuid.New(),
		Pending:                    1,
		PendingError:               0,
		Building:                   1,
		BuildingError:              0,
		DownloadingModel:           1,
		DownloadingModelError:      0,
		DownloadingContainer:       1,
		DownloadingContainerError:  0,
		InitializingContainer:      1,
		InitializingContainerError: 0,
		Ready:                      1,
		// Active:                     0,
		RequestingTermination: 0,
		Destroyed:             0,
	}, nil
}

func (p *passDBHandler) GetInstanceEvent(context.Context, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	ste, _ := types.NewStageTransitionEvent(
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		uuid.New(),
		uuid.New(),
		uuid.New().String(),
		"test",
		"test",
		time.Now(),
		json.RawMessage(`{"test": "test"}`))
	return ste, nil
}

func (p *passDBHandler) Close() error {
	return nil
}

// Interface assertion for V1
var _ data_access.DBHandler = (*passDBHandler)(nil)

type PassFixture struct{}

func (f *PassFixture) InitConns() Connections {
	return Connections{
		DbHandler: &passDBHandler{},
		// dbHandlerV2 is nil for V1 tests
	}
}

// Fail Fixtures ====================================================================

// failDBHandler implements DBHandler returning generic internal server errors.
type failDBHandler struct{}

func (p *failDBHandler) CheckStatus(context.Context) map[string]error {
	return map[string]error{
		"db": fmt.Errorf("failed to connect to db"),
	}
}

func (p *failDBHandler) WriteStageTransitionEvent(_ context.Context, ste types.StageTransitionEvent) error {
	return fmt.Errorf("DUPLICATE EVENT: event '%s' already exists for this instanceId: %v", ste.Event, ste.InstanceId)
}
func (p *failDBHandler) ListStageTransitionEvents(context.Context, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	return []types.StageTransitionEvent{types.ErrStageTransitionEvent}, fmt.Errorf("query failed: 'fail on purpose'")
}
func (p *failDBHandler) ListInstances(context.Context, uuid.UUID) ([]types.Instance, error) {
	return []types.Instance{}, fmt.Errorf("db error: fail on purpose")
}
func (p *failDBHandler) ArchiveInstanceStageTransitionEvents(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("db error: fail on purpose")
}
func (p *failDBHandler) ReadDeploymentStats(context.Context, uuid.UUID) (types.DeploymentStats, error) {
	return types.ErrDeploymentStats, fmt.Errorf("db error: fail on purpose")
}
func (p *failDBHandler) GetInstanceEvent(context.Context, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.ErrStageTransitionEvent, fmt.Errorf("db error: fail on purpose")
}

func (p *failDBHandler) ListInstancesPaginated(ctx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, fmt.Errorf("db error: fail on purpose")
}

// --- V2 Methods Removed ---

// --- Common ---
func (f *failDBHandler) Close() error {
	return errors.New("failed to close db connection")
}

// Interface assertion for V1
var _ data_access.DBHandler = (*failDBHandler)(nil)

// failDBQueryHandler implements DBHandler returning 'not found' errors.
type failDBQueryHandler struct{}

func (f *failDBQueryHandler) CheckStatus(context.Context) map[string]error {
	return map[string]error{
		"db": fmt.Errorf("failed to connect to db"),
	}
}

func (f *failDBQueryHandler) WriteStageTransitionEvent(context.Context, types.StageTransitionEvent) error {
	return errors.New("failed to write to db") // Specific write failure for this mock
}
func (p *failDBQueryHandler) ListStageTransitionEvents(context.Context, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	return []types.StageTransitionEvent{types.ErrStageTransitionEvent}, fmt.Errorf("not found")
}
func (p *failDBQueryHandler) ListInstances(context.Context, uuid.UUID) ([]types.Instance, error) {
	return []types.Instance{}, fmt.Errorf("not found")
}
func (p *failDBQueryHandler) ArchiveInstanceStageTransitionEvents(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("not found")
}
func (p *failDBQueryHandler) ReadDeploymentStats(context.Context, uuid.UUID) (types.DeploymentStats, error) {
	return types.ErrDeploymentStats, fmt.Errorf("not found")
}
func (p *failDBQueryHandler) GetInstanceEvent(context.Context, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.ErrStageTransitionEvent, fmt.Errorf("not found")
}

func (p *failDBQueryHandler) ListInstancesPaginated(ctx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, fmt.Errorf("not found")
}

// --- V2 Methods Removed ---

// --- Common ---
func (f *failDBQueryHandler) Close() error {
	return errors.New("failed to close db connection")
}

// Interface assertion for V1
var _ data_access.DBHandler = (*failDBQueryHandler)(nil)

// failDbDeleteQueryHandler simulates 'not found' for delete/archive, but success for list (to allow testing archive logic).
type failDbDeleteQueryHandler struct{}

func (f *failDbDeleteQueryHandler) CheckStatus(context.Context) map[string]error {
	return map[string]error{
		"db": fmt.Errorf("failed to connect to db"),
	}
}

func (f *failDbDeleteQueryHandler) WriteStageTransitionEvent(context.Context, types.StageTransitionEvent) error {
	return errors.New("failed to write to db")
}
func (p *failDbDeleteQueryHandler) ListStageTransitionEvents(context.Context, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	// V1 List needed for V1 archive tests
	ste, _ := types.NewStageTransitionEvent("NCA", uuid.New(), uuid.New(), "INSTANCE", "pending", "test", time.Now(), json.RawMessage(`{}`))
	return []types.StageTransitionEvent{ste}, nil
}
func (p *failDbDeleteQueryHandler) ListInstances(context.Context, uuid.UUID) ([]types.Instance, error) {
	// Return successful query to sets up the V1 archive call
	return []types.Instance{{InstanceId: uuid.New().String()}}, nil
}
func (p *failDbDeleteQueryHandler) ArchiveInstanceStageTransitionEvents(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("not found") // Specific failure for this mock
}
func (p *failDbDeleteQueryHandler) ReadDeploymentStats(context.Context, uuid.UUID) (types.DeploymentStats, error) {
	return types.ErrDeploymentStats, fmt.Errorf("not found")
}
func (p *failDbDeleteQueryHandler) GetInstanceEvent(context.Context, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.ErrStageTransitionEvent, fmt.Errorf("not found")
}

func (p *failDbDeleteQueryHandler) ListInstancesPaginated(ctx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, fmt.Errorf("not found")
}

// --- V2 Methods Removed ---

// --- Common ---
func (f *failDbDeleteQueryHandler) Close() error {
	return errors.New("failed to close db connection")
}

// Interface assertion for V1
var _ data_access.DBHandler = (*failDbDeleteQueryHandler)(nil)

// failDbDeleteHandler simulates generic internal errors during delete/archive, but success for list.
type failDbDeleteHandler struct{}

func (f *failDbDeleteHandler) CheckStatus(context.Context) map[string]error {
	return map[string]error{
		"db": fmt.Errorf("failed to connect to db"),
	}
}

func (f *failDbDeleteHandler) WriteStageTransitionEvent(context.Context, types.StageTransitionEvent) error {
	return errors.New("failed to write to db")
}
func (p *failDbDeleteHandler) ListStageTransitionEvents(context.Context, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	// Return success to allow testing V1 archive logic
	ste, _ := types.NewStageTransitionEvent("NCA", uuid.New(), uuid.New(), "INSTANCE", "pending", "test", time.Now(), json.RawMessage(`{}`))
	return []types.StageTransitionEvent{ste}, nil
}
func (p *failDbDeleteHandler) ListInstances(context.Context, uuid.UUID) ([]types.Instance, error) {
	// Return successful query to sets up the V1 archive call
	return []types.Instance{{InstanceId: uuid.New().String()}}, nil
}
func (p *failDbDeleteHandler) ArchiveInstanceStageTransitionEvents(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("fail on purpose") // Specific failure for this mock
}
func (p *failDbDeleteHandler) ReadDeploymentStats(context.Context, uuid.UUID) (types.DeploymentStats, error) {
	return types.ErrDeploymentStats, fmt.Errorf("fail on purpose")
}
func (p *failDbDeleteHandler) GetInstanceEvent(context.Context, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.ErrStageTransitionEvent, fmt.Errorf("query failed: 'fail on purpose'")
}

func (p *failDbDeleteHandler) ListInstancesPaginated(ctx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, fmt.Errorf("db error: fail on purpose")
}

// --- V2 Methods Removed ---

// --- Common ---
func (f *failDbDeleteHandler) Close() error {
	return errors.New("failed to close db connection")
}

// Interface assertion for V1
var _ data_access.DBHandler = (*failDbDeleteHandler)(nil)

type FailFixtureBadTopic struct{}

func (f *FailFixtureBadTopic) InitConns() Connections {
	return Connections{
		DbHandler: &failDBHandler{},
	}
}

type FailFixtureV1Topic struct{}

func (f *FailFixtureV1Topic) InitConns() Connections {
	return Connections{
		DbHandler: &failDBHandler{},
	}
}

type QueryFailFixture struct{}

func (f *QueryFailFixture) InitConns() Connections {
	return Connections{
		DbHandler: &failDBQueryHandler{},
	}
}

// FailFixtureBadDelete uses failDbDeleteHandler
type FailFixtureBadDelete struct{}

func (f *FailFixtureBadDelete) InitConns() Connections {
	return Connections{
		DbHandler: &failDbDeleteHandler{},
		// msgHandler logic...
	}
}

// FailFixtureBadDataJson uses failDBQueryHandler (could be more specific if needed)
type FailFixtureBadDataJson struct{}

func (f *FailFixtureBadDataJson) InitConns() Connections {
	return Connections{
		DbHandler: &failDBQueryHandler{},
		// msgHandler logic...
	}
}

// FailFixtureBadDeleteQuery uses failDbDeleteQueryHandler
type FailFixtureBadDeleteQuery struct{}

func (f *FailFixtureBadDeleteQuery) InitConns() Connections {
	return Connections{
		DbHandler: &failDbDeleteQueryHandler{},
		// msgHandler logic...
	}
}

// Table Tests =====================================================================
func TestJWTMiddleware(t *testing.T) {
	// Generate a test RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NoError(t, err)
	publicKey := &privateKey.PublicKey

	// Create a test JWKS endpoint
	jwks := map[string]interface{}{
		"keys": []interface{}{
			map[string]interface{}{
				"kty": "RSA",
				"kid": "test-kid",
				"use": "sig",
				"n":   encodeBase64URL(publicKey.N.Bytes()),
				"e":   encodeBase64URL(big.NewInt(int64(publicKey.E)).Bytes()),
			},
		},
	}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksServer.Close()

	// Create a logger for the test
	logger := otelzap.New(zaptest.NewLogger(t))

	// Prepare the JWT middleware options
	opts := middleware.JWTParserOptions{
		JwksURL:           jwksServer.URL,
		Method:            jwt.SigningMethodRS256,
		Issuer:            "https://issuer.example.com",
		Audience:          "event-ledger",
		TenantClaim:       "tenant_id",
		RequireExpiration: true,
	}
	jwkCache := jwk.NewCache(context.Background())
	jwtMiddleware := middleware.NewParseJWTMiddleware(opts, jwkCache)

	// Create a test router with the middleware
	router := mux.NewRouter()
	router.Use(jwtMiddleware)
	router.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Protected content"))
	})

	tests := []struct {
		name           string
		token          string
		expectedStatus int
	}{
		{
			name:           "No Token",
			token:          "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Invalid Token",
			token:          "invalid-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Valid Token",
			token: createTestToken(privateKey, "test-kid", map[string]interface{}{
				"scopes":    []string{"read"},
				"iss":       "https://issuer.example.com",
				"aud":       "event-ledger",
				"exp":       time.Now().Add(time.Hour).Unix(),
				"tenant_id": "test-tenant",
			}),
			expectedStatus: http.StatusOK,
		},
		{
			name: "Missing Expiration",
			token: createTestToken(privateKey, "test-kid", map[string]interface{}{
				"scopes":    []string{"read"},
				"iss":       "https://issuer.example.com",
				"aud":       "event-ledger",
				"tenant_id": "test-tenant",
			}),
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Wrong Audience",
			token: createTestToken(privateKey, "test-kid", map[string]interface{}{
				"scopes":    []string{"read"},
				"iss":       "https://issuer.example.com",
				"aud":       "another-service",
				"exp":       time.Now().Add(time.Hour).Unix(),
				"tenant_id": "test-tenant",
			}),
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Missing Tenant",
			token: createTestToken(privateKey, "test-kid", map[string]interface{}{
				"scopes": []string{"read"},
				"iss":    "https://issuer.example.com",
				"aud":    "event-ledger",
				"exp":    time.Now().Add(time.Hour).Unix(),
			}),
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/protected", nil)

			// Create a trace logger and add it to the context
			traceLogger := logging.NewTraceLogger(req.Context(), logger)
			ctx := context.WithValue(req.Context(), logging.LoggerKey, traceLogger)
			req = req.WithContext(ctx)

			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			assert.Equal(t, tt.expectedStatus, res.Code)
		})
	}
}

func createTestToken(privateKey *rsa.PrivateKey, kid string, claims map[string]interface{}) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims(claims))
	token.Header["kid"] = kid
	tokenString, _ := token.SignedString(privateKey)
	return tokenString
}

func encodeBase64URL(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

func TestPostStageTransitionEvent(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		wantBody   string
		postData   string
	}{
		{
			name:       "SuccessfulPostFunctionVersionIdInstanceId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusAccepted,
			wantBody:   "",
			postData: `{
			  "ncaId": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "functionVersionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "instanceId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "event": "pending",
			  "eventType": "test_post_functionVersionId_instanceId",
			  "timestamp": "2025-01-09T18:34:37.810129+00:00",
			  "details": {"instanceId": "test_post_functionVersionId_instanceId"}
			}`,
		},
		{
			name:       "Bad event",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "",
			postData: `{
			  "ncaId": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "functionVersionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "instanceId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "event": "test_post_functionVersionId_instanceId",
			  "eventType": "test_post_functionVersionId_instanceId",
			  "timestamp": "2025-01-09T18:34:37.810129+00:00",
			  "details": {"instanceId": "test_post_functionVersionId_instanceId"}
			}`,
		},
		{
			name:       "Duplicate event",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusConflict,
			wantBody:   "",
			postData: `{
			  "ncaId": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "functionVersionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "instanceId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "event": "building",
			  "eventType": "test_post_functionVersionId_instanceId",
			  "timestamp": "2025-01-09T18:34:37.810129+00:00",
			  "details": {"instanceId": "test_post_functionVersionId_instanceId"}
			}`,
		},
		{
			name:       "EmptyPostData",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "",
			postData:   `{}`,
		},
		{
			name:       "InvalidPostData",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "",
			postData:   `{`,
		},
		{
			name:       "UnsuccessfulPostFunctionVersionIdInstanceId",
			fixture:    &FailFixtureBadDelete{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Fail this",
			postData: `{
			  "ncaId": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "functionVersionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "instanceId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "event": "pending",
			  "eventType": "test_post_functionVersionId_instanceId",
			  "timestamp": "2025-01-09T18:34:37.810129+00:00",
			  "details": {"instanceId": "test_post_functionVersionId_instanceId"}
			}`,
		},
		{
			name:       "UnsuccessfulPostFunctionDetailsJson",
			fixture:    &FailFixtureBadDataJson{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "'details' field is not valid json",
			postData: `{
			  "ncaId": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			  "functionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "functionVersionId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "instanceId": "93787d8a-0a60-4f44-b121-572d2fae61c6",
			  "event": "pending",
			  "eventType": "test_post_functionVersionId_instanceId",
			  "timestamp": "2025-01-09T18:34:37.810129+00:00",
			  "details": "invalid json"
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conns := tt.fixture.InitConns()
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()

			version := testutils.InitTestVersion(t)
			httpConfig := config.DefaultHTTPConfig()
			server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			reader := strings.NewReader(tt.postData)

			// the POST verb here is used for consistency with the API
			// and has no bearing on the routing of a request at this level
			req := NewRequestWithLogger("POST", "/v1/ledger/versions/93787d8a-0a60-4f44-b121-572d2fae61c6/instances/93787d8a-0a60-4f44-b121-572d2fae61c6", reader, logger)

			rr := httptest.NewRecorder()

			server.PostStageTransitionEvent(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("TestSend got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
			}

			if tt.wantStatus != http.StatusInternalServerError {
				if !strings.Contains(rr.Body.String(), tt.wantBody) {
					t.Errorf("TestSend got '%v', but wanted it to contain '%v'", rr.Body.String(), tt.wantBody)
				}
			}

			if tt.wantStatus == http.StatusAccepted {
				events := publisher.GetEvents()
				assert.Equal(t, 1, len(events))
			}
		})
	}
}

func NewRequestWithLogger(verb string, url string, reader *strings.Reader, logger *otelzap.Logger) *http.Request {
	req, _ := http.NewRequest(verb, url, reader)
	requestLogger := logging.NewTraceLogger(req.Context(), logger)
	// Attach the logger to the request's context
	ctx := context.WithValue(req.Context(), logging.LoggerKey, requestLogger)
	req = req.WithContext(ctx)
	return req
}

func TestListStageTransitionEvents(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		fnVerId    string
		instanceId string
		wantBody   string
	}{
		{
			name:       "Get Success",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			wantBody:   "functionVersionId",
		},
		{
			name:       "Bad FnVerId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    "X",
			instanceId: uuid.New().String(),
			wantBody:   "",
		},
		{
			name:       "Bad InstanceId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    uuid.New().String(),
			instanceId: "X",
			wantBody:   "",
		},
		{
			name:       "Get Fail",
			fixture:    &QueryFailFixture{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			wantBody:   "failed to list stage transition event",
		},
		{
			name:       "Query Fail",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			wantBody:   "failed to list stage transition event",
		}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conns := tt.fixture.InitConns()
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			httpConfig := config.DefaultHTTPConfig()
			server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v1/ledger/versions/%s/instances/%s", tt.fnVerId, tt.instanceId)
			// Use empty reader for GET request
			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			rr := httptest.NewRecorder()

			vars := map[string]string{
				"functionVersionId": tt.fnVerId,
				"instanceId":        tt.instanceId,
			}
			req = mux.SetURLVars(req, vars)

			server.ListStageTransitionEvents(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("TestGet got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusInternalServerError {
				if !strings.Contains(rr.Body.String(), tt.wantBody) {
					t.Errorf("TestGet got '%v', but wanted it to contain '%v'", rr.Body.String(), tt.wantBody)
				}
			}

		})
	}
}

func TestListInstances(t *testing.T) {
	tests := []struct {
		name        string
		fixture     interface{ InitConns() Connections }
		wantStatus  int
		wantAmount  int
		queryParams string
		wantBody    string
		fnVerId     string
	}{
		{
			name:       "List Success",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			wantAmount: 3,
			wantBody:   "instanceId",
			fnVerId:    uuid.New().String(),
		},
		{
			name:        "List Filter Success",
			fixture:     &PassFixture{},
			wantStatus:  http.StatusOK,
			wantAmount:  1,
			queryParams: "?eventFilter=pending",
			wantBody:    "instanceId",
			fnVerId:     uuid.New().String(),
		},
		{
			name:        "List Stage Filter Success",
			fixture:     &PassFixture{},
			wantStatus:  http.StatusOK,
			wantAmount:  2,
			queryParams: "?stageFilter=pending",
			wantBody:    "instanceId",
			fnVerId:     uuid.New().String(),
		},
		{
			name:       "Bad Request",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			wantBody:   "functionVersionId",
			fnVerId:    "bad_uuid",
		},
		{
			name:       "Query Fail",
			fixture:    &QueryFailFixture{},
			wantStatus: http.StatusNotFound,
			wantBody:   "",
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Internal Fail",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "",
			fnVerId:    uuid.New().String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conns := tt.fixture.InitConns()
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			httpConfig := config.DefaultHTTPConfig()
			server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v1/ledger/versions/%s/instances%s", tt.fnVerId, tt.queryParams)
			// Use empty reader for GET request
			req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
			rr := httptest.NewRecorder()
			vars := map[string]string{
				"functionVersionId": tt.fnVerId,
			}
			req = mux.SetURLVars(req, vars)
			server.ListInstances(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("TestList got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusInternalServerError {
				if !strings.Contains(rr.Body.String(), tt.wantBody) {
					t.Errorf("TestGet got '%v', but wanted it to contain '%v'", rr.Body.String(), tt.wantBody)
				}
			}
			body, _ := io.ReadAll(rr.Body)
			var results []types.Instance
			json.Unmarshal(body, &results)
			if len(results) != tt.wantAmount {
				t.Errorf("Expected %d results but got %d", tt.wantAmount, len(results))
			}
		})
	}
}

func TestArchiveInstanceStageTransitionEvents(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		fnVerId    string
		instanceId string
	}{
		{
			name:       "Archive Success",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
		},
		{
			name:       "Bad FnVerId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    "bad_uuid",
			instanceId: uuid.New().String(),
		},
		{
			name:       "Bad InstanceId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    uuid.New().String(),
			instanceId: "bad_uuid",
		},
		{
			name:       "Query Fail",
			fixture:    &QueryFailFixture{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
		},
		{
			name:       "Internal Fail",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conns := tt.fixture.InitConns()
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			httpConfig := config.DefaultHTTPConfig()
			server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v1/ledger/versions/%s/instances/%s", tt.fnVerId, tt.instanceId)
			// Use empty reader for DELETE request
			req := NewRequestWithLogger("DELETE", url, strings.NewReader(""), logger)
			rr := httptest.NewRecorder()
			vars := map[string]string{
				"functionVersionId": tt.fnVerId,
				"instanceId":        tt.instanceId,
			}
			req = mux.SetURLVars(req, vars)
			server.ArchiveInstanceStageTransitionEvents(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("TestArchiveInstanceStageTransitionEvents got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestArchiveFunctionVersionStageTransitionEvents(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		fnVerId    string
	}{
		{
			name:       "Archive Success",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Bad FnVerId",
			fixture:    &PassFixture{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    "bad_uuid",
		},
		{
			name:       "Query Fail",
			fixture:    &QueryFailFixture{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "List instances fail",
			fixture:    &FailFixtureV1Topic{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Internal Fail",
			fixture:    &FailFixtureBadDelete{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Not Found on Archive call",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conns := tt.fixture.InitConns()
			logger := testutils.InitTestLogger(t)
			publisher := testutils.NewTestPublisher()
			version := testutils.InitTestVersion(t)
			httpConfig := config.DefaultHTTPConfig()
			server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

			url := fmt.Sprintf("/v1/ledger/versions/%s/instances", tt.fnVerId)
			// Use empty reader for DELETE request
			req := NewRequestWithLogger("DELETE", url, strings.NewReader(""), logger)
			rr := httptest.NewRecorder()
			vars := map[string]string{
				"functionVersionId": tt.fnVerId,
			}
			req = mux.SetURLVars(req, vars)
			server.ArchiveFunctionVersionStageTransitionEvents(rr, req)
			if rr.Code != tt.wantStatus {
				t.Errorf("TestArchiveInstanceStageTransitionEvents got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
			}
		})
	}
}

func TestCors(t *testing.T) {
	tests := []struct {
		name string
		verb string
	}{
		{
			name: "preflight",
			verb: "OPTIONS",
		},
		{
			name: "GET",
			verb: "GET",
		},
		{
			name: "POST",
			verb: "POST",
		},
		// {
		//	name: "PUT",
		//	verb: "PUT",
		// },
		{
			name: "DELETE",
			verb: "DELETE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test router with the middleware
			router := mux.NewRouter()
			router.Use(middleware.EnableCORS)
			router.HandleFunc("/CORS", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			reader := strings.NewReader("") // Use empty reader for CORS tests
			req := httptest.NewRequest(tt.verb, "/CORS", reader)

			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)

			response := res.Result()
			// Check for a headers
			headers := map[string]string{
				"Access-Control-Allow-Headers": "Content-Type, Authorization",
				"Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
			}
			for k, v := range headers {
				if headerValue := response.Header.Get(k); headerValue != v {
					t.Errorf("%s does not equal %s in the response.", k, v)
				}
			}

		})
	}
}

func TestGetDeploymentStats(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		fnVerId    string
	}{
		{
			name:       "Get Deployment Stats",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Bad Get Deployment Stats",
			fixture:    &FailFixtureBadDelete{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Get Deployment Stats Query Fail",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
		},
		{
			name:       "Get Deployment Stats Bad Function Version Id",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    "00",
		},
	}
	for _, tt := range tests {
		conns := tt.fixture.InitConns()
		logger := testutils.InitTestLogger(t)
		publisher := testutils.NewTestPublisher()
		version := testutils.InitTestVersion(t)
		httpConfig := config.DefaultHTTPConfig()
		server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

		url := fmt.Sprintf("/v1/ledger/versions/%s/stats", tt.fnVerId)
		// Use empty reader for GET request
		req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
		rr := httptest.NewRecorder()
		vars := map[string]string{
			"functionVersionId": tt.fnVerId,
		}
		req = mux.SetURLVars(req, vars)
		server.GetDeploymentStats(rr, req)
		if rr.Code != tt.wantStatus {
			t.Errorf("TestGetDeploymentStats got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
		}
	}
}

func TestGetInstanceEvent(t *testing.T) {
	tests := []struct {
		name       string
		fixture    interface{ InitConns() Connections }
		wantStatus int
		fnVerId    string
		instanceId string
		event      string
	}{
		{
			name:       "Get Instance Event",
			fixture:    &PassFixture{},
			wantStatus: http.StatusOK,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			event:      "pending",
		},
		{
			name:       "Bad Get Instance Event",
			fixture:    &FailFixtureBadDelete{},
			wantStatus: http.StatusInternalServerError,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			event:      "pending",
		},
		{
			name:       "Get Instance Event Query Fail",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusNotFound,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			event:      "pending",
		},
		{
			name:       "Get Instance Event Bad Function Version Id",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    "00",
			instanceId: uuid.New().String(),
			event:      "pending",
		},
		{
			name:       "Get Instance Event Bad Instance Id",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    uuid.New().String(),
			instanceId: "00",
			event:      "pending",
		},
		{
			name:       "Get Instance Event Bad Event Type",
			fixture:    &FailFixtureBadDeleteQuery{},
			wantStatus: http.StatusBadRequest,
			fnVerId:    uuid.New().String(),
			instanceId: uuid.New().String(),
			event:      "bad_event",
		},
	}
	for _, tt := range tests {
		conns := tt.fixture.InitConns()
		logger := testutils.InitTestLogger(t)
		publisher := testutils.NewTestPublisher()
		version := testutils.InitTestVersion(t)
		httpConfig := config.DefaultHTTPConfig()
		server := NewServer(conns, logger, publisher, version, &httpConfig, config.GetDefaultPaginationConfig(), config.StatsConfig{})

		url := fmt.Sprintf("/v1/ledger/versions/%s/instances/%s/event/%s", tt.fnVerId, tt.instanceId, tt.event)
		// Use empty reader for GET request
		req := NewRequestWithLogger("GET", url, strings.NewReader(""), logger)
		rr := httptest.NewRecorder()
		vars := map[string]string{
			"functionVersionId": tt.fnVerId,
			"instanceId":        tt.instanceId,
			"event":             tt.event,
		}
		req = mux.SetURLVars(req, vars)
		server.GetInstanceEvent(rr, req)
		if rr.Code != tt.wantStatus {
			t.Errorf("TestGetInstanceEvent got '%v', but wanted '%v'", rr.Code, tt.wantStatus)
		}
	}
}
