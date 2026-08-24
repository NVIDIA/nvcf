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

// NOTE: Tests for V3 OTLP/K8s Events endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
)

// statsUpsertCall records an UpsertStatsV3 / UpsertFilteredStatsV3 invocation for assertions.
type statsUpsertCall struct {
	namespace string
	context   string
	eventName string
	timestamp time.Time
}

// Mock DBHandlerV2 for V3 tests
type mockDBHandlerV3 struct {
	storedEvents []struct {
		namespace string
		context   string
		eventName string
		source    string
		details   json.RawMessage
		timestamp time.Time
	}
	upsertStatsCalls         []statsUpsertCall
	upsertFilteredStatsCalls []statsUpsertCall
	getStatsCalls            int
	getFilteredStatsCalls    int
	storedStatsEvents        []data_access.EventV3UpsertRecord
	upsertEventCalls         int
	bulkUpsertEventsCalls    int
	bulkUpsertStatsCalls     int
	bulkUpsertEventsErr      error
	bulkUpsertStatsErr       error
}

// V3 methods
func (m *mockDBHandlerV3) UpsertEventV3(ctx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	m.upsertEventCalls++
	m.storedEvents = append(m.storedEvents, struct {
		namespace string
		context   string
		eventName string
		source    string
		details   json.RawMessage
		timestamp time.Time
	}{namespace, eventContext, eventName, source, details, timestamp})
	return nil
}

func (m *mockDBHandlerV3) UpsertStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	m.upsertStatsCalls = append(m.upsertStatsCalls, statsUpsertCall{namespace, eventContext, eventName, timestamp})
	return nil
}

func (m *mockDBHandlerV3) UpsertFilteredStatsV3(ctx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	m.upsertFilteredStatsCalls = append(m.upsertFilteredStatsCalls, statsUpsertCall{namespace, eventContext, eventName, timestamp})
	return nil
}

func (m *mockDBHandlerV3) BulkUpsertEventsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	m.bulkUpsertEventsCalls++
	if m.bulkUpsertEventsErr != nil {
		return m.bulkUpsertEventsErr
	}
	for _, e := range events {
		m.storedEvents = append(m.storedEvents, struct {
			namespace string
			context   string
			eventName string
			source    string
			details   json.RawMessage
			timestamp time.Time
		}{e.Namespace, e.Context, e.EventName, e.Source, e.Details, e.Timestamp})
	}
	return nil
}

func (m *mockDBHandlerV3) BulkUpsertStatsV3(ctx context.Context, events []data_access.EventV3UpsertRecord) error {
	m.bulkUpsertStatsCalls++
	if m.bulkUpsertStatsErr != nil {
		return m.bulkUpsertStatsErr
	}
	m.storedStatsEvents = append(m.storedStatsEvents, events...)
	return nil
}

func (m *mockDBHandlerV3) GetStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	m.getStatsCalls++
	// Return mock data based on namespace
	if namespace == "test-namespace" {
		now := time.Now()
		return []data_access.StatsV3Record{
			{Context: "pod-1", EventName: "pending", Timestamp: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
			{Context: "pod-2", EventName: "pending", Timestamp: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
			{Context: "pod-3", EventName: "ready", Timestamp: now, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		}, nil
	}
	if namespace == "empty-namespace" {
		return []data_access.StatsV3Record{}, nil
	}
	return nil, fmt.Errorf("namespace not found")
}

func (m *mockDBHandlerV3) GetFilteredStatsV3(ctx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	m.getFilteredStatsCalls++
	// Return data distinguishable from GetStatsV3 so view-routing tests can verify.
	if namespace == "test-namespace" {
		now := time.Now()
		return []data_access.StatsV3Record{
			{Context: "pod-1", EventName: "ready", Timestamp: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		}, nil
	}
	if namespace == "empty-namespace" {
		return []data_access.StatsV3Record{}, nil
	}
	return nil, fmt.Errorf("namespace not found")
}

func (m *mockDBHandlerV3) GetEventsV3(ctx context.Context, namespace, eventContext string) ([]data_access.EventV3Record, error) {
	// Return mock data based on namespace and context (canonical format)
	if namespace == "test-namespace" && eventContext == "instance_id=pod-1" {
		now := time.Now()
		details1, _ := json.Marshal(EventDetails{
			Severity:       "INFO",
			SeverityNumber: 9,
			Body:           "Pod created",
			Attributes:     map[string]any{"key1": "value1"},
		})
		details2, _ := json.Marshal(EventDetails{
			Severity:       "INFO",
			SeverityNumber: 9,
			Body:           "Pod ready",
			Attributes:     map[string]any{"key2": "value2"},
		})
		return []data_access.EventV3Record{
			{EventName: "ready", Details: details2, Timestamp: now, CreatedAt: now.Add(-time.Minute), UpdatedAt: now},
			{EventName: "pending", Details: details1, Timestamp: now.Add(-5 * time.Minute), CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)},
		}, nil
	}
	if namespace == "test-namespace" && eventContext == "instance_id=empty-pod" {
		return []data_access.EventV3Record{}, nil
	}
	// Empty context is allowed - return empty results
	if namespace == "test-namespace" && eventContext == "" {
		return []data_access.EventV3Record{}, nil
	}
	return nil, fmt.Errorf("context not found")
}

// V2 methods (stub implementations)
func (m *mockDBHandlerV3) WriteDeploymentStageTransitionEvent(context.Context, types.DeploymentStageTransitionEvent) error {
	return nil
}
func (m *mockDBHandlerV3) ListDeploymentStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) ([]types.StageTransitionEvent, error) {
	return nil, nil
}
func (m *mockDBHandlerV3) ListDeploymentInstances(context.Context, uuid.UUID, uuid.UUID) ([]types.Instance, error) {
	return nil, nil
}
func (m *mockDBHandlerV3) GetDeploymentInstanceEvent(context.Context, uuid.UUID, uuid.UUID, string, codex.Event) (types.StageTransitionEvent, error) {
	return types.StageTransitionEvent{}, nil
}
func (m *mockDBHandlerV3) ArchiveDeploymentInstanceStageTransitionEvents(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (m *mockDBHandlerV3) ReadDeploymentDeploymentStats(context.Context, uuid.UUID, uuid.UUID) (types.DeploymentStats, error) {
	return types.DeploymentStats{}, nil
}
func (m *mockDBHandlerV3) ListDeploymentInstancesPaginated(ctx context.Context, functionVersionId, deploymentId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	return data_access.PaginatedInstancesResponse{}, nil
}
func (m *mockDBHandlerV3) Close() error {
	return nil
}

// Helper to create OTLP LogRecord
func createOTLPLogRecord(eventName, namespace, source, instanceID string, extraAttrs map[string]string) *logsv1.LogRecord {
	attrs := []*commonv1.KeyValue{
		{Key: "event_name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: eventName}}},
		{Key: "namespace", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: namespace}}},
		{Key: "source", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: source}}},
	}

	// Add context fields to OTLP attributes
	if instanceID != "" {
		attrs = append(attrs, &commonv1.KeyValue{
			Key:   "instance_id",
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: instanceID}},
		})
	}

	for k, v := range extraAttrs {
		attrs = append(attrs, &commonv1.KeyValue{
			Key:   k,
			Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}},
		})
	}

	return &logsv1.LogRecord{
		TimeUnixNano:   uint64(time.Now().UnixNano()),
		SeverityText:   "INFO",
		SeverityNumber: logsv1.SeverityNumber_SEVERITY_NUMBER_INFO,
		Body:           &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: "Test event"}},
		Attributes:     attrs,
	}
}

func TestEventContextToCanonical_DotSeparatedInstanceID(t *testing.T) {
	instanceID := "00000000-0000-4000-8000-000000000001.synthetic-instance"

	canonical, err := eventContextToCanonical(ContextV3{InstanceID: instanceID})

	require.NoError(t, err)
	assert.Equal(t, "instance_id="+instanceID, canonical)
}

func TestEventContextToCanonical_DotsRemainInvalidForOtherContextFields(t *testing.T) {
	_, err := eventContextToCanonical(ContextV3{DeploymentID: "deployment.invalid"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid deployment_id")
}

// Test that namespace is required
func TestPostK8sEventV3_NamespaceRequired(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// Create OTLP request WITHOUT namespace
	req := &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							createOTLPLogRecord("pod.ready", " ", "kubernetes", "pod-123", nil), // Empty namespace
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	// Add logger to context (required by logging.GetLogger)
	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "namespace")
}

// Test that event_name is required
func TestPostK8sEventV3_EventNameRequired(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// Create OTLP request WITHOUT event_name
	req := &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							createOTLPLogRecord(" ", "tenant-123", "kubernetes", "pod-123", nil), // Empty event_name
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	// Add logger to context (required by logging.GetLogger)
	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostK8sEventV3_SourceRequired(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// Create OTLP request WITHOUT source
	req := &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							createOTLPLogRecord("pod.ready", "tenant-123", " ", "pod-123", nil), // Empty source
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "source")
}

// Test successful event storage
func TestPostK8sEventV3_Success(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// Create valid OTLP request
	req := &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{
				ScopeLogs: []*logsv1.ScopeLogs{
					{
						LogRecords: []*logsv1.LogRecord{
							createOTLPLogRecord("pod.ready", "tenant-123", "kubernetes", "pod-456", map[string]string{
								"pod_name": "my-pod",
								"custom":   "value",
							}),
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	require.NoError(t, err)

	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	// Add logger to context (required by logging.GetLogger)
	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, len(mockDB.storedEvents))

	// Verify stored event
	stored := mockDB.storedEvents[0]
	assert.Equal(t, "tenant-123", stored.namespace)
	assert.Equal(t, "instance_id=pod-456", stored.context) // Canonical format
	assert.Equal(t, "pod.ready", stored.eventName)

	// Verify details JSON does NOT contain duplicated fields
	var details map[string]any
	err = json.Unmarshal(stored.details, &details)
	require.NoError(t, err)

	// Should NOT have these (they're in columns)
	assert.NotContains(t, details, "event_name")
	assert.NotContains(t, details, "namespace")
	assert.NotContains(t, details, "context")

	// Should have OTLP metadata
	assert.Contains(t, details, "severity")
	assert.Contains(t, details, "body")

	// Should have extra attributes
	attrs := details["attributes"].(map[string]any)
	assert.Equal(t, "my-pod", attrs["pod_name"])
	assert.Equal(t, "value", attrs["custom"])
}

// Test that only protobuf is accepted
func TestPostK8sEventV3_RejectsJSON(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader([]byte(`{"test": "json"}`)))
	httpReq.Header.Set("Content-Type", "application/json")

	// Add logger to context (required by logging.GetLogger)
	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	assert.Contains(t, w.Body.String(), "protobuf")
}

func TestPostK8sEventV3_RejectsOversizedDecompressedBody(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: &mockDBHandlerV3{}},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	body := bytes.NewReader(make([]byte, maxDecompressedBodySize+1))
	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", body)
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	ctx := httpReq.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	httpReq = httpReq.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func postOTLPRequest(t *testing.T, server *Server, req *collectorlogsv1.ExportLogsServiceRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := proto.Marshal(req)
	require.NoError(t, err)
	httpReq := httptest.NewRequest("POST", "/v3/ledger/k8s-events", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	logger := testutils.InitTestLogger(t)
	ctx := context.WithValue(httpReq.Context(), logging.LoggerKey, logging.NewTraceLogger(httpReq.Context(), logger))
	httpReq = httpReq.WithContext(ctx)
	w := httptest.NewRecorder()
	server.PostK8sEventV3(w, httpReq)
	return w
}

func TestPostK8sEventV3_BulkUpsertEventsFailure(t *testing.T) {
	mockDB := &mockDBHandlerV3{bulkUpsertEventsErr: fmt.Errorf("cassandra down")}
	server := newServerWithMock(t, mockDB)

	w := postOTLPRequest(t, server, newOTLPRequest(
		createOTLPLogRecord("pod.ready", "ns", "src", "pod-1", nil),
	))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "cassandra down")
}

func TestPostK8sEventV3_StatsFailurePartialSuccess(t *testing.T) {
	mockDB := &mockDBHandlerV3{bulkUpsertStatsErr: fmt.Errorf("stats down")}
	server := newServerWithMock(t, mockDB, "pod.ready") // only pod.ready is stats-enabled

	// pod.pending is not stats-enabled so it succeeds; pod.ready fails stats
	w := postOTLPRequest(t, server, newOTLPRequest(
		createOTLPLogRecord("pod.ready", "ns", "src", "pod-1", nil),
		createOTLPLogRecord("pod.pending", "ns", "src", "pod-2", nil),
	))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp EventResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "partial_success", resp.Status)
	assert.Equal(t, 1, resp.SuccessCount)
	assert.Equal(t, 1, resp.FailureCount)
}

func TestPostK8sEventV3_StatsFailureAllFail(t *testing.T) {
	mockDB := &mockDBHandlerV3{bulkUpsertStatsErr: fmt.Errorf("stats down")}
	server := newServerWithMock(t, mockDB) // all events are stats-enabled (no filter)

	w := postOTLPRequest(t, server, newOTLPRequest(
		createOTLPLogRecord("pod.ready", "ns", "src", "pod-1", nil),
	))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "stats down")
}

// Test extractK8sEvent
func TestExtractK8sEvent(t *testing.T) {
	lr := createOTLPLogRecord("pod.ready", "tenant-123", "kubernetes", "pod-456", map[string]string{
		"extra_field": "extra_value",
	})

	event, err := extractK8sEvent(lr)
	require.NoError(t, err)

	// Check struct fields
	assert.Equal(t, "pod.ready", event.EventName)
	assert.Equal(t, "tenant-123", event.Namespace)
	assert.Equal(t, "instance_id=pod-456", event.Context) // Canonical format
	assert.NotZero(t, event.Timestamp)

	// Check details JSON
	var details map[string]any
	err = json.Unmarshal(event.DetailsJSON, &details)
	require.NoError(t, err)

	// Verify no duplication
	assert.NotContains(t, details, "event_name")
	assert.NotContains(t, details, "namespace")
	assert.NotContains(t, details, "context")

	// Verify OTLP metadata
	assert.Equal(t, "INFO", details["severity"])
	assert.Equal(t, "Test event", details["body"])

	// Verify extra attributes
	attrs := details["attributes"].(map[string]any)
	assert.Equal(t, "extra_value", attrs["extra_field"])
}

// Test extractCloudEvent validates source is required
func TestExtractCloudEvent_SourceRequired(t *testing.T) {
	ce := cloudevents.NewEvent()
	ce.SetID("test-id")
	ce.SetType("test.event")
	ce.SetSource("") // Empty source
	ce.SetExtension("namespace", "test-namespace")

	_, err := extractCloudEvent(&ce)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field: source")
}

// Test extractCloudEvent validates type is required
func TestExtractCloudEvent_TypeRequired(t *testing.T) {
	ce := cloudevents.NewEvent()
	ce.SetID("test-id")
	ce.SetType("") // Empty type
	ce.SetSource("/test")
	ce.SetExtension("namespace", "test-namespace")

	_, err := extractCloudEvent(&ce)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field: type")
}

// Test extractCloudEvent validates id is required
func TestExtractCloudEvent_IdRequired(t *testing.T) {
	ce := cloudevents.NewEvent()
	ce.SetID("") // Empty id
	ce.SetType("test.event")
	ce.SetSource("/test")
	ce.SetExtension("namespace", "test-namespace")

	_, err := extractCloudEvent(&ce)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field: id")
}

// ======================
// CloudEvents Endpoint Validation Tests
// ======================

// Helper to create a test server and execute a CloudEvents request
func executeCloudEventsRequest(t *testing.T, body []byte, contentType string) (*httptest.ResponseRecorder, *mockDBHandlerV3) {
	t.Helper()
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("POST", "/v3/ledger/cloudevents", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)

	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	server.PostCloudEventV3(w, req)
	return w, mockDB
}

// Test PostCloudEventV3 returns specific error for missing specversion (single event)
func TestPostCloudEventV3_MissingSpecversion(t *testing.T) {
	body := []byte(`{
		"type": "com.nvidia.test",
		"source": "/test",
		"id": "event-123",
		"namespace": "test-namespace"
	}`)

	w, _ := executeCloudEventsRequest(t, body, "application/cloudevents+json")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "specversion")
}

// Test PostCloudEventV3 returns specific error for missing specversion (batch format)
func TestPostCloudEventV3_BatchMissingSpecversion(t *testing.T) {
	body := []byte(`[
		{
			"type": "com.nvidia.test",
			"source": "/test",
			"id": "event-123",
			"namespace": "test-namespace"
		}
	]`)

	w, _ := executeCloudEventsRequest(t, body, "application/cloudevents-batch+json")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "specversion")
}

func TestProcessCloudEvents_UsesBulkPersistence(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newServerWithMock(t, mockDB)
	ctx := makeStoreCtx(server)

	makeCloudEvent := func(id, eventType, instanceID string) *cloudevents.Event {
		event := cloudevents.NewEvent()
		event.SetSpecVersion(cloudevents.VersionV1)
		event.SetID(id)
		event.SetType(eventType)
		event.SetSource("/test")
		event.SetTime(time.Now())
		event.SetExtension("namespace", "test-namespace")
		event.SetExtension("instanceId", instanceID)
		require.NoError(t, event.SetData(cloudevents.ApplicationJSON, map[string]string{"status": "ok"}))
		return &event
	}

	result := server.processCloudEvents(ctx, []*cloudevents.Event{
		makeCloudEvent("event-1", "pod.ready", "00000000-0000-4000-8000-000000000001.synthetic-instance"),
		makeCloudEvent("event-2", "pod.pending", "pod-2"),
	})

	assert.Equal(t, 2, result.SuccessCount)
	assert.Equal(t, 0, result.FailureCount)
	assert.Equal(t, 0, mockDB.upsertEventCalls, "per-event LWT path must not be used")
	assert.Equal(t, 1, mockDB.bulkUpsertEventsCalls)
	assert.Equal(t, 1, mockDB.bulkUpsertStatsCalls)
	require.Len(t, mockDB.storedEvents, 2)
	contexts := []string{mockDB.storedEvents[0].context, mockDB.storedEvents[1].context}
	assert.Contains(t, contexts,
		"instance_id=00000000-0000-4000-8000-000000000001.synthetic-instance",
	)
}

// Test GetStatsV3 success
func TestGetStatsV3_Success(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL param (mux.Vars)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response StatsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "test-namespace", response.Namespace)
	assert.Equal(t, 3, response.Summary.TotalContexts)
	assert.Equal(t, 2, response.Summary.ByEvent["pending"])
	assert.Equal(t, 1, response.Summary.ByEvent["ready"])
	assert.Len(t, response.Contexts, 3)

	// Verify flat list contains correct data
	pendingCount := 0
	readyCount := 0
	for _, ctx := range response.Contexts {
		assert.NotEmpty(t, ctx.Context)
		assert.NotEmpty(t, ctx.EventName)
		assert.NotZero(t, ctx.Timestamp)
		assert.NotZero(t, ctx.FirstSeen)
		if ctx.EventName == "pending" {
			pendingCount++
		} else if ctx.EventName == "ready" {
			readyCount++
		}
	}
	assert.Equal(t, 2, pendingCount)
	assert.Equal(t, 1, readyCount)
}

// Test GetStatsV3 with event filter
func TestGetStatsV3_WithFilter(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats?eventFilter=ready", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL param (mux.Vars)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response StatsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, 1, response.Summary.TotalContexts)
	assert.Equal(t, 1, response.Summary.ByEvent["ready"])
	assert.Len(t, response.Contexts, 1)

	// Verify only "ready" events are returned (filtered out "pending")
	for _, ctx := range response.Contexts {
		assert.Equal(t, "ready", ctx.EventName)
	}
	assert.NotContains(t, response.Summary.ByEvent, "pending")
}

// Test GetStatsV3 empty namespace
func TestGetStatsV3_EmptyNamespace(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/empty-namespace/stats", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL param (mux.Vars)
	req = mux.SetURLVars(req, map[string]string{"namespace": "empty-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response StatsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, 0, response.Summary.TotalContexts)
	assert.Empty(t, response.Contexts)
}

// Test GetStatsV3 DB error
func TestGetStatsV3_DBError(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/nonexistent/stats", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL param (mux.Vars)
	req = mux.SetURLVars(req, map[string]string{"namespace": "nonexistent"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "Internal Server Error")
}

// ======================
// GetEventsV3 Tests
// ======================

// Test GetEventsV3 success case
func TestGetEventsV3_Success(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// Use query params for context: ?instance_id=pod-1
	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/events?instance_id=pod-1", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL params (only namespace)
	req = mux.SetURLVars(req, map[string]string{
		"namespace": "test-namespace",
	})

	w := httptest.NewRecorder()
	server.GetEventsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response EventsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "test-namespace", response.Namespace)
	assert.Equal(t, "instance_id=pod-1", response.Context) // Canonical format
	assert.Len(t, response.Events, 2)

	// Verify events are in descending timestamp order (ready then pending)
	assert.Equal(t, "ready", response.Events[0].EventName)
	assert.Equal(t, "pending", response.Events[1].EventName)

	// Verify details were properly unmarshaled
	assert.Equal(t, "INFO", response.Events[0].Details.Severity)
	assert.Equal(t, int32(9), response.Events[0].Details.SeverityNumber)
	assert.Equal(t, "Pod ready", response.Events[0].Details.Body)
	assert.Equal(t, "value2", response.Events[0].Details.Attributes["key2"])

	// Verify timestamps
	assert.NotZero(t, response.Events[0].Timestamp)
	assert.NotZero(t, response.Events[0].CreatedAt)
	assert.NotZero(t, response.Events[0].UpdatedAt)
}

// Test GetEventsV3 with empty result
func TestGetEventsV3_EmptyResult(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/events?instance_id=empty-pod", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL params (only namespace)
	req = mux.SetURLVars(req, map[string]string{
		"namespace": "test-namespace",
	})

	w := httptest.NewRecorder()
	server.GetEventsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response EventsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.Equal(t, "test-namespace", response.Namespace)
	assert.Equal(t, "instance_id=empty-pod", response.Context) // Canonical format
	assert.Empty(t, response.Events)
}

// Test GetEventsV3 DB error
func TestGetEventsV3_DBError(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/nonexistent/events?instance_id=pod-x", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL params (only namespace)
	req = mux.SetURLVars(req, map[string]string{
		"namespace": "nonexistent",
	})

	w := httptest.NewRecorder()
	server.GetEventsV3(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "Internal Server Error")
}

// Test GetEventsV3 missing namespace
func TestGetEventsV3_MissingNamespace(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	req := httptest.NewRequest("GET", "/v3/ledger/namespace//events?instance_id=pod-1", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL params with empty namespace
	req = mux.SetURLVars(req, map[string]string{
		"namespace": "",
	})

	w := httptest.NewRecorder()
	server.GetEventsV3(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "namespace is required")
}

// Test GetEventsV3 missing context (no query params) - context is optional
func TestGetEventsV3_MissingContext(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	logger := testutils.InitTestLogger(t)
	server := NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		config.StatsConfig{},
	)

	// No query params = empty context
	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/events", nil)

	// Add logger to context
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, logger))
	req = req.WithContext(ctx)

	// Add URL params (only namespace)
	req = mux.SetURLVars(req, map[string]string{
		"namespace": "test-namespace",
	})

	w := httptest.NewRecorder()
	server.GetEventsV3(w, req)

	// Empty context is allowed - should return 200 with empty events
	assert.Equal(t, http.StatusOK, w.Code)

	var response EventsV3Response
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "test-namespace", response.Namespace)
	assert.Equal(t, "", response.Context)
	assert.Empty(t, response.Events)
}

// ============================================================================
// Filtered stats view tests.
// ============================================================================

// newTestServerWithStatsConfig builds a Server pre-wired with the given StatsConfig
// for the filtered stats tests below. Mirrors the inline setup used elsewhere in this
// file but lets each test pick its own filtered-view allowlist.
func newTestServerWithStatsConfig(t *testing.T, mockDB *mockDBHandlerV3, statsCfg config.StatsConfig) *Server {
	t.Helper()
	logger := testutils.InitTestLogger(t)
	return NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		statsCfg,
	)
}

func createOTLPLogRecordAt(eventName, namespace, source, instanceID string, ts time.Time) *logsv1.LogRecord {
	lr := createOTLPLogRecord(eventName, namespace, source, instanceID, nil)
	lr.TimeUnixNano = uint64(ts.UnixNano())
	return lr
}

func newOTLPRequest(records ...*logsv1.LogRecord) *collectorlogsv1.ExportLogsServiceRequest {
	return &collectorlogsv1.ExportLogsServiceRequest{
		ResourceLogs: []*logsv1.ResourceLogs{
			{ScopeLogs: []*logsv1.ScopeLogs{
				{LogRecords: records},
			}},
		},
	}
}

func newServerWithMock(t *testing.T, mockDB *mockDBHandlerV3, statsEventNames ...string) *Server {
	t.Helper()
	logger := testutils.InitTestLogger(t)
	var statsCfg config.StatsConfig
	if len(statsEventNames) > 0 {
		statsCfg = config.StatsConfig{StatsEnabledEventNames: statsEventNames}
	}
	return NewServer(
		Connections{DbHandlerV2: mockDB},
		logger,
		nil,
		"test",
		&config.HTTPClientConfig{},
		config.PaginationConfig{},
		statsCfg,
	)
}

// makeStoreCtx returns a context carrying a trace logger, as required by storeK8sEvent's
// logging.GetLogger call.
func makeStoreCtx(server *Server) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
}

// makeEvent returns a minimal valid EventV3 for ingest-path tests.
func makeEvent(eventName string) *EventV3 {
	return &EventV3{
		EventName:   eventName,
		Namespace:   "ns-1",
		Source:      "kubernetes",
		Context:     "instance_id=pod-1",
		Timestamp:   time.Now(),
		DetailsJSON: json.RawMessage(`{"body":"x"}`),
	}
}

// Empty filtered-view allowlist -> filtered stats table never written, regardless of event.
// stats_v3 still written (existing filter, empty=all).
func TestStoreK8sEvent_FilteredStats_EmptyList_Skips(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{})

	for _, name := range []string{"ready", "heartbeat", "anything-else"} {
		err := server.storeK8sEvent(makeStoreCtx(server), makeEvent(name))
		require.NoError(t, err)
	}

	assert.Equal(t, 3, len(mockDB.storedEvents), "events_v3 always written")
	assert.Equal(t, 3, len(mockDB.upsertStatsCalls), "stats_v3 always written (empty stats filter = all)")
	assert.Equal(t, 0, len(mockDB.upsertFilteredStatsCalls), "filtered stats table never written when allowlist is empty")
}

// Non-empty filtered-view allowlist + matching event -> filtered stats table written with correct args.
func TestStoreK8sEvent_FilteredStats_MatchingEvent_Upserts(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{
		FilteredStatsEnabledEventNames: []string{"ready", "destroyed"},
	})

	err := server.storeK8sEvent(makeStoreCtx(server), makeEvent("ready"))
	require.NoError(t, err)

	require.Equal(t, 1, len(mockDB.upsertFilteredStatsCalls))
	call := mockDB.upsertFilteredStatsCalls[0]
	assert.Equal(t, "ns-1", call.namespace)
	assert.Equal(t, "instance_id=pod-1", call.context)
	assert.Equal(t, "ready", call.eventName)
}

// Non-empty filtered-view allowlist + non-matching event -> filtered stats table NOT written.
// stats_v3 still written.
func TestStoreK8sEvent_FilteredStats_NonMatchingEvent_Skips(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{
		FilteredStatsEnabledEventNames: []string{"ready", "destroyed"},
	})

	err := server.storeK8sEvent(makeStoreCtx(server), makeEvent("heartbeat"))
	require.NoError(t, err)

	assert.Equal(t, 1, len(mockDB.upsertStatsCalls), "stats_v3 still written")
	assert.Equal(t, 0, len(mockDB.upsertFilteredStatsCalls), "filtered stats table skipped for non-matching event")
}

// Default view (no ?view param) -> reads stats_v3, not the filtered stats table.
func TestGetStatsV3_DefaultView_ReadsStatsV3(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{})

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
	req = req.WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, mockDB.getStatsCalls)
	assert.Equal(t, 0, mockDB.getFilteredStatsCalls)

	var resp StatsV3Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// stats_v3 canned data returns 3 contexts
	assert.Equal(t, 3, resp.Summary.TotalContexts)
}

// ?view=filtered -> reads the filtered stats table, not stats_v3.
func TestGetStatsV3_FilteredView_ReadsFilteredTable(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{
		FilteredStatsEnabledEventNames: []string{"ready"},
	})

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats?view=filtered", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
	req = req.WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, mockDB.getStatsCalls)
	assert.Equal(t, 1, mockDB.getFilteredStatsCalls)

	var resp StatsV3Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// filtered stats table canned data returns just 1 context (pod-1, ready)
	assert.Equal(t, 1, resp.Summary.TotalContexts)
	require.Len(t, resp.Contexts, 1)
	assert.Equal(t, "ready", resp.Contexts[0].EventName)
}

// The legacy ?view=ngc value remains a compatibility alias for the filtered view.
func TestGetStatsV3_LegacyFilteredViewAlias_ReadsFilteredTable(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{
		FilteredStatsEnabledEventNames: []string{"ready"},
	})

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats?view=ngc", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
	req = req.WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 0, mockDB.getStatsCalls)
	assert.Equal(t, 1, mockDB.getFilteredStatsCalls)

	var resp StatsV3Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.Summary.TotalContexts)
	require.Len(t, resp.Contexts, 1)
	assert.Equal(t, "ready", resp.Contexts[0].EventName)
}

func TestGetStatsV3_FilteredViewDisabled_Returns404(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{})

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats?view=filtered", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
	req = req.WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "filtered stats view is not enabled")
	assert.Equal(t, 0, mockDB.getStatsCalls)
	assert.Equal(t, 0, mockDB.getFilteredStatsCalls)
}

// ?view=garbage -> 400, no DbHandler call.
func TestGetStatsV3_InvalidView_Returns400(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{})

	req := httptest.NewRequest("GET", "/v3/ledger/namespace/test-namespace/stats?view=garbage", nil)
	ctx := req.Context()
	ctx = context.WithValue(ctx, logging.LoggerKey, logging.NewTraceLogger(ctx, server.logger))
	req = req.WithContext(ctx)
	req = mux.SetURLVars(req, map[string]string{"namespace": "test-namespace"})

	w := httptest.NewRecorder()
	server.GetStatsV3(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "view")
	assert.Equal(t, 0, mockDB.getStatsCalls)
	assert.Equal(t, 0, mockDB.getFilteredStatsCalls)
}

func TestProcessOTLPEvents(t *testing.T) {
	now := time.Now()
	older := now.Add(-time.Minute)

	ctx := context.WithValue(context.Background(), logging.LoggerKey,
		logging.NewTraceLogger(context.Background(), testutils.InitTestLogger(t)))

	t.Run("deduplicates events before bulk write", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{}
		server := newServerWithMock(t, mockDB)

		req := newOTLPRequest(
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", older),
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", now), // same key, newer timestamp
		)

		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 1, result.SuccessCount)
		assert.Equal(t, 0, result.FailureCount)
		require.Len(t, mockDB.storedEvents, 1)
		assert.Equal(t, now.UnixNano(), mockDB.storedEvents[0].timestamp.UnixNano())
	})

	t.Run("all events fail when BulkUpsertEventsV3 returns error", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{bulkUpsertEventsErr: fmt.Errorf("cassandra down")}
		server := newServerWithMock(t, mockDB)

		req := newOTLPRequest(
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", now),
			createOTLPLogRecordAt("pod.pending", "ns", "src", "pod-2", now),
		)

		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 0, result.SuccessCount)
		assert.Equal(t, 2, result.FailureCount)
		assert.ErrorContains(t, result.LastError, "cassandra down")
	})

	t.Run("stats failure fails only stats-enabled events", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{bulkUpsertStatsErr: fmt.Errorf("stats unavailable")}
		server := newServerWithMock(t, mockDB, "pod.ready") // only pod.ready is stats-enabled

		req := newOTLPRequest(
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", now),
			createOTLPLogRecordAt("pod.pending", "ns", "src", "pod-2", now),
		)

		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 1, result.SuccessCount) // pod.pending succeeded (not stats-enabled)
		assert.Equal(t, 1, result.FailureCount) // pod.ready failed stats
		assert.ErrorContains(t, result.LastError, "stats unavailable")
	})

	t.Run("invalid events counted as failures, valid events succeed", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{}
		server := newServerWithMock(t, mockDB)

		req := newOTLPRequest(
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", now),
			createOTLPLogRecordAt(" ", "ns", "src", "pod-2", now), // invalid: blank event_name
		)

		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 1, result.SuccessCount)
		assert.Equal(t, 1, result.FailureCount)
		assert.Len(t, mockDB.storedEvents, 1)
	})

	t.Run("only stats-enabled events sent to BulkUpsertStatsV3", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{}
		server := newServerWithMock(t, mockDB, "pod.ready") // only pod.ready is stats-enabled

		req := newOTLPRequest(
			createOTLPLogRecordAt("pod.ready", "ns", "src", "pod-1", now),
			createOTLPLogRecordAt("pod.pending", "ns", "src", "pod-2", now),
		)

		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 2, result.SuccessCount)
		require.Len(t, mockDB.storedStatsEvents, 1)
		assert.Equal(t, "pod.ready", mockDB.storedStatsEvents[0].EventName)
	})

	t.Run("empty request returns zero counts", func(t *testing.T) {
		mockDB := &mockDBHandlerV3{}
		server := newServerWithMock(t, mockDB)

		req := newOTLPRequest()
		result := server.processOTLPEvents(ctx, req)

		assert.Equal(t, 0, result.SuccessCount)
		assert.Equal(t, 0, result.FailureCount)
		assert.Empty(t, mockDB.storedEvents)
	})
}

func TestDeduplicateEvents(t *testing.T) {
	now := time.Now()
	older := now.Add(-time.Minute)

	t.Run("keeps latest timestamp on duplicate key", func(t *testing.T) {
		events := []*EventV3{
			{Namespace: "ns", Context: "ctx", EventName: "pod.ready", Timestamp: older},
			{Namespace: "ns", Context: "ctx", EventName: "pod.ready", Timestamp: now},
		}
		result := deduplicateEvents(events)
		require.Len(t, result, 1)
		assert.Equal(t, now, result[0].Timestamp)
	})

	t.Run("retains distinct keys", func(t *testing.T) {
		events := []*EventV3{
			{Namespace: "ns", Context: "ctx", EventName: "pod.ready", Timestamp: now},
			{Namespace: "ns", Context: "ctx", EventName: "pod.pending", Timestamp: now},
			{Namespace: "ns", Context: "ctx2", EventName: "pod.ready", Timestamp: now},
		}
		result := deduplicateEvents(events)
		assert.Len(t, result, 3)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		result := deduplicateEvents(nil)
		assert.Empty(t, result)
	})

	t.Run("single event returned as-is", func(t *testing.T) {
		events := []*EventV3{
			{Namespace: "ns", Context: "ctx", EventName: "pod.ready", Timestamp: now},
		}
		result := deduplicateEvents(events)
		require.Len(t, result, 1)
		assert.Equal(t, now, result[0].Timestamp)
	})
}
