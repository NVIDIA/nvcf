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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/gorilla/mux"
	"github.com/mitchellh/mapstructure"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	collectorlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
)

var (
	// contextFieldPattern validates context field values (alphanumeric and dashes only)
	contextFieldPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)
	// instanceIDFieldPattern additionally permits dot-separated instance ID segments.
	instanceIDFieldPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)*$`)
	namespaceFieldPattern  = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

	ErrMissingEventName = errors.New("missing required field: event_name")
	ErrMissingNamespace = errors.New("missing required field: namespace")
	ErrMissingSource    = errors.New("missing required field: source")
	ErrMissingType      = errors.New("missing required field: type")

	MaxContextLength = 1024
)

const (
	defaultStatsView        = ""
	filteredStatsView       = "filtered"
	legacyFilteredStatsView = "ngc"
	maxDecompressedBodySize = 10 * 1024 * 1024
)

// ProcessedEventSummary represents a summary of a successfully processed event
type ProcessedEventSummary struct {
	Namespace string `json:"namespace"`         // Tenant identifier (required)
	Context   string `json:"context,omitempty"` // Optional: instance/context identifier
	Name      string `json:"name"`              // Event name (required)
	Timestamp string `json:"timestamp"`         // ISO 8601 timestamp
}

// EventResponse represents a successful event processing response
type EventResponse struct {
	Status         string                  `json:"status"`
	SuccessCount   int                     `json:"success_count"`
	FailureCount   int                     `json:"failure_count"`
	TotalProcessed int                     `json:"total_processed"`
	EventsSample   []ProcessedEventSummary `json:"events_sample,omitempty"`
}

// EventDetails represents the details blob stored in the database
// Contains OTLP metadata and extra attributes that don't map to known fields
type EventDetails struct {
	Severity       string         `json:"severity,omitempty"`
	SeverityNumber int32          `json:"severity_number,omitempty"`
	Body           any            `json:"body,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
}

// ContextStats represents a single context (pod/instance) with its latest event info
type ContextStats struct {
	Context   string    `json:"context"`
	EventName string    `json:"event_name"`
	Timestamp time.Time `json:"timestamp"`
	FirstSeen time.Time `json:"first_seen"`
}

// StatsSummary provides aggregate counts of contexts grouped by event
type StatsSummary struct {
	TotalContexts int            `json:"total_contexts"`
	ByEvent       map[string]int `json:"by_event"`
}

// StatsV3Response is the response structure for GET /v3/ledger/namespace/{namespace}/stats
type StatsV3Response struct {
	Namespace string         `json:"namespace"`
	Summary   StatsSummary   `json:"summary"`
	Contexts  []ContextStats `json:"contexts"`
}

// EventV3Item represents a single event in the EventsV3Response
type EventV3Item struct {
	EventName string       `json:"event_name"`
	Source    string       `json:"source"`
	Timestamp time.Time    `json:"timestamp"`
	Details   EventDetails `json:"details"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// EventsV3Response is the response structure for GET /v3/ledger/namespace/{namespace}/context/{context}/events
type EventsV3Response struct {
	Namespace string        `json:"namespace"`
	Context   string        `json:"context"`
	Events    []EventV3Item `json:"events"`
}

// Context field names used in the canonical context string and as query params.
const (
	contextFieldClusterID          = "cluster_id"
	contextFieldDeploymentID       = "deployment_id"
	contextFieldGPUSpecificationID = "gpu_specification_id"
	contextFieldInstanceID         = "instance_id"
	contextFieldResourceID         = "resource_id"
)

// ContextV3 represents the context components that identify the scope of an event
// This is the internal representation, not tied to any wire format
type ContextV3 struct {
	InstanceID         string
	DeploymentID       string
	GPUSpecificationID string
	ClusterID          string
	// ResourceID is a generic, optional context field. It lets a producer supply
	// a unique identifier for events that have no other distinguishing context
	// field (e.g. an ICMSRequest, keyed by its request id), so distinct resources
	// do not collapse onto the same dedup key. It is empty for Pod events, which
	// are already uniquely identified by instance_id.
	ResourceID string
}

// cloudEventWireFormat represents the CloudEvents extensions wire format
// Used to deserialize CloudEvent extensions before converting to internal format
// Note: CloudEvents spec forbids underscores in extension names, so we use camelCase
type cloudEventWireFormat struct {
	Namespace          string         `mapstructure:"namespace"`
	InstanceID         string         `mapstructure:"instanceId"`
	DeploymentID       string         `mapstructure:"deploymentId"`
	GPUSpecificationID string         `mapstructure:"gpuSpecificationId"`
	ClusterID          string         `mapstructure:"clusterId"`
	ResourceID         string         `mapstructure:"resourceId"`
	UnmappedExtensions map[string]any `mapstructure:",remain"`
}

// otlpAttributesWireFormat represents the OTLP attributes wire format
// Used to deserialize OTLP attributes before converting to internal format
type otlpAttributesWireFormat struct {
	EventName          string         `mapstructure:"event_name"`
	Namespace          string         `mapstructure:"namespace"`
	Source             string         `mapstructure:"source"`
	InstanceID         string         `mapstructure:"instance_id"`
	DeploymentID       string         `mapstructure:"deployment_id"`
	GPUSpecificationID string         `mapstructure:"gpu_specification_id"`
	ClusterID          string         `mapstructure:"cluster_id"`
	ResourceID         string         `mapstructure:"resource_id"`
	UnmappedAttributes map[string]any `mapstructure:",remain"`
}

// EventV3 represents an event in our internal format
// This is what gets stored in the database and passed around internally
// Not tied to any specific wire format (OTLP, CloudEvents, etc.)
type EventV3 struct {
	EventName   string
	Namespace   string
	Source      string // Event source identifier (e.g., "kubernetes", "nvcf")
	Context     string // Canonical context string (e.g., "instanceid=pod-1")
	Timestamp   time.Time
	DetailsJSON json.RawMessage // Event details as JSON blob
}

// Validate checks required fields, patterns, and lengths
func (e *EventV3) Validate() error {
	if strings.TrimSpace(e.EventName) == "" {
		return ErrMissingEventName
	}
	if len(e.EventName) > MaxContextLength {
		return fmt.Errorf("event_name exceeds max length of %d", MaxContextLength)
	}
	if strings.TrimSpace(e.Namespace) == "" {
		return ErrMissingNamespace
	}
	if !namespaceFieldPattern.MatchString(e.Namespace) {
		return fmt.Errorf("invalid namespace: must contain only alphanumeric characters and dashes")
	}
	if len(e.Namespace) > MaxContextLength {
		return fmt.Errorf("namespace exceeds max length of %d", MaxContextLength)
	}
	if strings.TrimSpace(e.Source) == "" {
		return ErrMissingSource
	}
	if len(e.Source) > MaxContextLength {
		return fmt.Errorf("source exceeds max length of %d", MaxContextLength)
	}
	return nil
}

// PostK8sEventV3 handles K8s events in OTLP format (both JSON and protobuf)
func (s *Server) PostK8sEventV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	// Validate Content-Type before reading body
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/x-protobuf" {
		logger.WarnContext(traceCtx, "Rejected non-protobuf request",
			zap.String("content_type", contentType))
		sendProblemDetail(w, http.StatusUnsupportedMediaType, "Unsupported Media Type",
			"K8s events endpoint only accepts protobuf format (Content-Type: application/x-protobuf)")
		return
	}

	logger.InfoContext(traceCtx, "Processing OTLP K8s Events",
		zap.String("content_type", contentType))

	// The gzip middleware has already decompressed the request. Apply a second
	// limit here so compressed input cannot expand into an unbounded allocation.
	r.Body = http.MaxBytesReader(w, r.Body, maxDecompressedBodySize)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to read request body", zap.Error(err))
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			sendProblemDetail(w, http.StatusRequestEntityTooLarge, "Payload Too Large", "Decompressed request body exceeds 10 MiB")
			return
		}
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "Failed to read request body")
		return
	}

	var req collectorlogsv1.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		logger.ErrorContext(traceCtx, "Failed to parse protobuf",
			zap.Error(err),
			zap.Int("body_size", len(body)))
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "Invalid protobuf format")
		return
	}

	logger.InfoContext(traceCtx, "Successfully parsed protobuf OTLP request")

	// Process the batch of events
	result := s.processOTLPEvents(traceCtx, &req)

	// Send response
	s.sendEventResponse(w, traceCtx, result)
}

// EventProcessingResult holds the results of processing a batch of events
type EventProcessingResult struct {
	SuccessCount    int
	FailureCount    int
	ProcessedEvents []ProcessedEventSummary
	LastError       error
}

// processOTLPEvents extracts and stores K8s events from OTLP log records
func (s *Server) processOTLPEvents(traceCtx context.Context, req *collectorlogsv1.ExportLogsServiceRequest) EventProcessingResult {
	logger := logging.GetLogger(traceCtx)
	result := EventProcessingResult{ProcessedEvents: make([]ProcessedEventSummary, 0, 64)}

	var events []*EventV3
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				event, err := extractK8sEvent(lr)
				if err != nil {
					logger.WarnContext(traceCtx, "Skipping event", zap.Error(err))
					result.FailureCount++
					result.LastError = err
					continue
				}
				if !middleware.IsTenantAuthorized(traceCtx, event.Namespace) {
					err := errors.New("tenant is not authorized")
					logger.WarnContext(traceCtx, "Skipping unauthorized tenant event")
					result.FailureCount++
					result.LastError = err
					continue
				}
				events = append(events, event)
			}
		}
	}

	events = deduplicateEvents(events)

	records := make([]data_access.EventV3UpsertRecord, len(events))
	for i, e := range events {
		records[i] = data_access.EventV3UpsertRecord{
			Namespace: e.Namespace,
			Context:   e.Context,
			EventName: e.EventName,
			Source:    e.Source,
			Details:   e.DetailsJSON,
			Timestamp: e.Timestamp,
		}
	}

	if len(records) > 0 {
		if err := s.conns.DbHandlerV2.BulkUpsertEventsV3(traceCtx, records); err != nil {
			logger.ErrorContext(traceCtx, "Failed to bulk upsert events", zap.Error(err))
			result.FailureCount += len(events)
			result.LastError = err
			return result
		}

		var statsRecords []data_access.EventV3UpsertRecord
		for _, r := range records {
			if s.isStatsEnabled(r.EventName) {
				statsRecords = append(statsRecords, r)
			}
		}
		if len(statsRecords) > 0 {
			if err := s.conns.DbHandlerV2.BulkUpsertStatsV3(traceCtx, statsRecords); err != nil {
				logger.ErrorContext(traceCtx, "Failed to bulk upsert stats", zap.Error(err))
				result.FailureCount += len(statsRecords)
				result.SuccessCount = len(events) - len(statsRecords)
				result.LastError = err
				return result
			}
		}
	}

	for _, e := range events {
		result.SuccessCount++
		result.ProcessedEvents = append(result.ProcessedEvents, ProcessedEventSummary{
			Namespace: e.Namespace,
			Context:   e.Context,
			Name:      e.EventName,
			Timestamp: e.Timestamp.Format(time.RFC3339),
		})
	}
	return result
}

// deduplicateEvents keeps the latest event per (namespace, context, event_name).
func deduplicateEvents(events []*EventV3) []*EventV3 {
	type dedupKey struct {
		namespace string
		context   string
		eventName string
	}
	seen := make(map[dedupKey]*EventV3, len(events))
	for _, e := range events {
		key := dedupKey{namespace: e.Namespace, context: e.Context, eventName: e.EventName}
		if existing, ok := seen[key]; !ok || e.Timestamp.After(existing.Timestamp) {
			seen[key] = e
		}
	}
	result := make([]*EventV3, 0, len(seen))
	for _, e := range seen {
		result = append(result, e)
	}
	return result
}

// eventContextToCanonical converts a ContextV3 struct to a canonical string representation
// Format: key1=value1,key2=value2 in a fixed field order:
// cluster_id, deployment_id, gpu_specification_id, instance_id, resource_id.
// Validates that values contain only alphanumeric characters and dashes; instance IDs
// may also contain dots between non-empty segments. Empty fields are omitted, so events
// that do not set resource_id (e.g. Pods) produce the same context string as before it
// was introduced.
func eventContextToCanonical(eventContext ContextV3) (string, error) {
	// Helper to validate field values
	validate := func(name, value string) error {
		pattern := contextFieldPattern
		allowedCharacters := "alphanumeric characters and dashes"
		if name == contextFieldInstanceID {
			pattern = instanceIDFieldPattern
			allowedCharacters = "alphanumeric characters, dashes, and dots between segments"
		}

		if value != "" && !pattern.MatchString(value) {
			return fmt.Errorf("invalid %s '%s': must contain only %s", name, value, allowedCharacters)
		}

		if len(value) > MaxContextLength {
			return fmt.Errorf("invalid %s '%s': value is too long", name, value)
		}

		return nil
	}

	// Ordered fields: order defines the canonical string layout and must stay
	// stable, since the context string is the storage/dedup key.
	fields := []struct {
		name  string
		value string
	}{
		{contextFieldClusterID, eventContext.ClusterID},
		{contextFieldDeploymentID, eventContext.DeploymentID},
		{contextFieldGPUSpecificationID, eventContext.GPUSpecificationID},
		{contextFieldInstanceID, eventContext.InstanceID},
		{contextFieldResourceID, eventContext.ResourceID},
	}

	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		if err := validate(f.name, f.value); err != nil {
			return "", err
		}
		if f.value != "" {
			parts = append(parts, f.name+"="+f.value)
		}
	}

	return strings.Join(parts, ","), nil
}

// extractK8sEvent converts an OTLP log record to EventV3
// Expected OTLP attributes:
//   - event_name (string): Event type
//   - namespace (string): Tenant identifier
//   - source (string): Event source identifier
//   - Context fields (optional): instance_id, deployment_id, gpu_specification_id, cluster_id
//   - resource_id (optional): generic unique identifier for events that have no
//     other distinguishing context field (e.g. an ICMSRequest keyed by its request id).
func extractK8sEvent(lr *logsv1.LogRecord) (*EventV3, error) {
	// Step 1: Convert OTLP protobuf attributes to map
	attrs := make(map[string]any)
	for _, attr := range lr.Attributes {
		attrs[attr.Key] = extractValue(attr.Value)
	}

	// Step 2: Decode map into OTLP wire format struct
	var wireFormat otlpAttributesWireFormat
	if err := mapstructure.Decode(attrs, &wireFormat); err != nil {
		return nil, fmt.Errorf("failed to decode OTLP attributes: %w", err)
	}

	// Step 3: Convert wire format to internal context representation
	contextV3 := ContextV3{
		InstanceID:         wireFormat.InstanceID,
		DeploymentID:       wireFormat.DeploymentID,
		GPUSpecificationID: wireFormat.GPUSpecificationID,
		ClusterID:          wireFormat.ClusterID,
		ResourceID:         wireFormat.ResourceID,
	}

	canonicalContext, err := eventContextToCanonical(contextV3)
	if err != nil {
		return nil, fmt.Errorf("failed to convert context to canonical format: %w", err)
	}

	// Set timestamp from OTLP log record
	var timestamp time.Time
	if lr.TimeUnixNano != 0 {
		timestamp = time.Unix(0, int64(lr.TimeUnixNano))
	} else {
		timestamp = time.Now()
	}

	// Build details struct with OTLP metadata + unmapped attributes
	details := EventDetails{
		Severity:       lr.SeverityText,
		SeverityNumber: int32(lr.SeverityNumber),
		Attributes:     wireFormat.UnmappedAttributes,
	}
	if lr.Body != nil {
		details.Body = extractValue(lr.Body)
	}

	// Marshal details to JSON
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal details: %w", err)
	}

	// Step 4: Build internal EventV3 struct
	event := &EventV3{
		EventName:   wireFormat.EventName,
		Namespace:   wireFormat.Namespace,
		Source:      wireFormat.Source,
		Context:     canonicalContext,
		Timestamp:   timestamp,
		DetailsJSON: detailsJSON,
	}

	if err := event.Validate(); err != nil {
		return nil, err
	}
	return event, nil
}

// extractCloudEvent converts a CloudEvent into our internal EventV3 format
// CloudEvents extensions are mapped via mapstructure to:
//   - namespace (required)
//   - Context fields (optional, camelCase): instanceId, deploymentId, gpuSpecificationId, clusterId
//     Note: CloudEvents spec forbids underscores in extension names, so we use camelCase
func extractCloudEvent(ce *cloudevents.Event) (*EventV3, error) {
	// Validate required CloudEvents fields per spec (using CloudEvents field names in errors)
	if strings.TrimSpace(ce.ID()) == "" {
		return nil, errors.New("missing required field: id")
	}
	if strings.TrimSpace(ce.Type()) == "" {
		return nil, errors.New("missing required field: type")
	}
	if strings.TrimSpace(ce.Source()) == "" {
		return nil, errors.New("missing required field: source")
	}

	// Decode CloudEvent extensions into wire format struct
	var wireFormat cloudEventWireFormat
	if err := mapstructure.Decode(ce.Extensions(), &wireFormat); err != nil {
		return nil, fmt.Errorf("failed to decode CloudEvent extensions: %w", err)
	}

	// Convert wire format to internal context representation
	contextV3 := ContextV3{
		InstanceID:         wireFormat.InstanceID,
		DeploymentID:       wireFormat.DeploymentID,
		GPUSpecificationID: wireFormat.GPUSpecificationID,
		ClusterID:          wireFormat.ClusterID,
		ResourceID:         wireFormat.ResourceID,
	}

	canonicalContext, err := eventContextToCanonical(contextV3)
	if err != nil {
		return nil, fmt.Errorf("failed to convert context to canonical format: %w", err)
	}

	details := EventDetails{
		Body:       ce.Data(),
		Attributes: wireFormat.UnmappedExtensions,
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal details: %w", err)
	}

	event := &EventV3{
		EventName:   ce.Type(),
		Namespace:   wireFormat.Namespace,
		Source:      ce.Source(),
		Context:     canonicalContext,
		Timestamp:   ce.Time(),
		DetailsJSON: detailsJSON,
	}

	if err := event.Validate(); err != nil {
		return nil, err
	}
	return event, nil
}

// processCloudEvents validates a CloudEvents request and persists accepted events in bulk.
// Response counts continue to describe the input events, while duplicate storage keys are
// reduced to their latest timestamp before persistence.
func (s *Server) processCloudEvents(traceCtx context.Context, cloudEvents []*cloudevents.Event) EventProcessingResult {
	logger := logging.GetLogger(traceCtx)
	result := EventProcessingResult{ProcessedEvents: make([]ProcessedEventSummary, 0, len(cloudEvents))}

	acceptedEvents := make([]*EventV3, 0, len(cloudEvents))
	for _, cloudEvent := range cloudEvents {
		if cloudEvent == nil {
			err := errors.New("CloudEvent must not be null")
			logger.WarnContext(traceCtx, "Skipping null CloudEvent", zap.Error(err))
			result.FailureCount++
			result.LastError = err
			continue
		}

		event, err := extractCloudEvent(cloudEvent)
		if err != nil {
			logger.WarnContext(traceCtx, "Skipping event", zap.Error(err))
			result.FailureCount++
			result.LastError = err
			continue
		}

		if !middleware.IsTenantAuthorized(traceCtx, event.Namespace) {
			err := errors.New("tenant is not authorized")
			logger.WarnContext(traceCtx, "Skipping unauthorized tenant event")
			result.FailureCount++
			result.LastError = err
			continue
		}

		acceptedEvents = append(acceptedEvents, event)
	}

	storageEvents := deduplicateEvents(acceptedEvents)
	records := make([]data_access.EventV3UpsertRecord, len(storageEvents))
	for i, event := range storageEvents {
		records[i] = data_access.EventV3UpsertRecord{
			Namespace: event.Namespace,
			Context:   event.Context,
			EventName: event.EventName,
			Source:    event.Source,
			Details:   event.DetailsJSON,
			Timestamp: event.Timestamp,
		}
	}

	if len(records) > 0 {
		if err := s.conns.DbHandlerV2.BulkUpsertEventsV3(traceCtx, records); err != nil {
			logger.ErrorContext(traceCtx, "Failed to bulk upsert CloudEvents", zap.Error(err))
			result.FailureCount += len(acceptedEvents)
			result.LastError = err
			return result
		}

		statsRecords := make([]data_access.EventV3UpsertRecord, 0, len(records))
		for _, record := range records {
			if s.isStatsEnabled(record.EventName) {
				statsRecords = append(statsRecords, record)
			}
		}
		if len(statsRecords) > 0 {
			if err := s.conns.DbHandlerV2.BulkUpsertStatsV3(traceCtx, statsRecords); err != nil {
				logger.ErrorContext(traceCtx, "Failed to bulk upsert CloudEvent stats", zap.Error(err))
				result.LastError = err
				for _, event := range acceptedEvents {
					if s.isStatsEnabled(event.EventName) {
						result.FailureCount++
						continue
					}
					s.completeCloudEvent(traceCtx, event, &result)
				}
				return result
			}
		}
	}

	for _, event := range acceptedEvents {
		s.completeCloudEvent(traceCtx, event, &result)
	}

	return result
}

// completeCloudEvent preserves filtered-view writes, which do not have a bulk interface yet,
// without putting event and primary-stats persistence back on the per-event LWT path.
func (s *Server) completeCloudEvent(traceCtx context.Context, event *EventV3, result *EventProcessingResult) {
	if s.isFilteredStatsEnabled(event.EventName) {
		if err := s.conns.DbHandlerV2.UpsertFilteredStatsV3(traceCtx, event.Namespace, event.Context, event.EventName, event.Timestamp); err != nil {
			logging.GetLogger(traceCtx).ErrorContext(traceCtx, "Failed to store event in filtered stats view", zap.Error(err))
			result.FailureCount++
			result.LastError = err
			return
		}
	}
	result.addProcessedEvent(event)
}

func (result *EventProcessingResult) addProcessedEvent(event *EventV3) {
	result.SuccessCount++
	result.ProcessedEvents = append(result.ProcessedEvents, ProcessedEventSummary{
		Namespace: event.Namespace,
		Context:   event.Context,
		Name:      event.EventName,
		Timestamp: event.Timestamp.Format(time.RFC3339),
	})
}

// storeK8sEvent persists an event to both events_v3 and optionally stats_v3
func (s *Server) storeK8sEvent(traceCtx context.Context, event *EventV3) error {
	logger := logging.GetLogger(traceCtx)

	// Store in events_v3 (retains all unique event_names per context)
	// DetailsJSON is already marshaled, just pass it through
	if err := s.conns.DbHandlerV2.UpsertEventV3(traceCtx, event.Namespace, event.Context, event.EventName, event.Source, event.DetailsJSON, event.Timestamp); err != nil {
		return fmt.Errorf("failed to store in events_v3: %w", err)
	}

	// Store in stats_v3 (only latest event per context) - if event is enabled
	if s.isStatsEnabled(event.EventName) {
		if err := s.conns.DbHandlerV2.UpsertStatsV3(traceCtx, event.Namespace, event.Context, event.EventName, event.Timestamp); err != nil {
			return fmt.Errorf("failed to store in stats_v3: %w", err)
		}
	} else {
		logger.DebugContext(traceCtx, "Skipping stats_v3 update - event not in stats-enabled list",
			zap.String("event_name", event.EventName))
	}

	// Store the latest event per context in the opt-in filtered stats view.
	if s.isFilteredStatsEnabled(event.EventName) {
		if err := s.conns.DbHandlerV2.UpsertFilteredStatsV3(traceCtx, event.Namespace, event.Context, event.EventName, event.Timestamp); err != nil {
			return fmt.Errorf("failed to store in filtered stats view: %w", err)
		}
	}

	return nil
}

// sendEventResponse sends the HTTP response for event processing
func (s *Server) sendEventResponse(w http.ResponseWriter, traceCtx context.Context, result EventProcessingResult) {
	logger := logging.GetLogger(traceCtx)

	// Prepare response based on results
	if result.SuccessCount == 0 && result.FailureCount == 0 {
		logger.ErrorContext(traceCtx, "No events found in OTLP request")
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "No events found in request")
		return
	}

	if result.SuccessCount == 0 && result.FailureCount > 0 {
		// Provide specific error detail if available, otherwise general message
		errMsg := "All events failed processing"
		if result.LastError != nil {
			errMsg = result.LastError.Error()
		}

		logger.ErrorContext(traceCtx, "All events failed",
			zap.Int("failed", result.FailureCount),
			zap.Error(result.LastError))
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", errMsg)
		return
	}

	// Success/partial success response
	status := "accepted"
	if result.FailureCount > 0 {
		status = "partial_success"
		logger.WarnContext(traceCtx, "Batch processing completed with some failures",
			zap.Int("success", result.SuccessCount),
			zap.Int("failed", result.FailureCount))
	} else {
		logger.InfoContext(traceCtx, "Batch processing completed successfully",
			zap.Int("events", result.SuccessCount))
	}

	response := EventResponse{
		Status:         status,
		SuccessCount:   result.SuccessCount,
		FailureCount:   result.FailureCount,
		TotalProcessed: result.SuccessCount + result.FailureCount,
	}

	// Include sample of processed events (first 10)
	if len(result.ProcessedEvents) > 0 {
		maxEvents := 10
		if len(result.ProcessedEvents) < maxEvents {
			maxEvents = len(result.ProcessedEvents)
		}
		response.EventsSample = result.ProcessedEvents[:maxEvents]
	}

	sendJSONResponse(w, http.StatusOK, response)
}

// extractValue converts OTLP protobuf AnyValue to native Go types
// This is necessary because OTLP uses protobuf oneof/union types that require type switching
func extractValue(val *commonv1.AnyValue) interface{} {
	if val == nil {
		return nil
	}

	switch v := val.Value.(type) {
	case *commonv1.AnyValue_StringValue:
		return v.StringValue
	case *commonv1.AnyValue_BoolValue:
		return v.BoolValue
	case *commonv1.AnyValue_IntValue:
		return v.IntValue
	case *commonv1.AnyValue_DoubleValue:
		return v.DoubleValue
	case *commonv1.AnyValue_BytesValue:
		return v.BytesValue
	case *commonv1.AnyValue_ArrayValue:
		if v.ArrayValue != nil {
			var arr []interface{}
			for _, elem := range v.ArrayValue.Values {
				arr = append(arr, extractValue(elem))
			}
			return arr
		}
	case *commonv1.AnyValue_KvlistValue:
		if v.KvlistValue != nil {
			m := make(map[string]any)
			for _, kv := range v.KvlistValue.Values {
				m[kv.Key] = extractValue(kv.Value)
			}
			return m
		}
	}
	return nil
}

// PostCloudEventV3 processes CloudEvents (batch or single)
func (s *Server) PostCloudEventV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	contentType := r.Header.Get("Content-Type")
	logger.InfoContext(traceCtx, "Processing CloudEvents",
		zap.String("content_type", contentType))

	// Parse CloudEvents based on Content-Type, streaming directly from request body
	// application/cloudevents-batch+json = batch
	// application/cloudevents+json = single event
	decoder := json.NewDecoder(r.Body)
	var events []*cloudevents.Event

	switch contentType {
	case "application/cloudevents-batch+json":
		// Batch format
		if err := decoder.Decode(&events); err != nil {
			logger.ErrorContext(traceCtx, "Failed to parse CloudEvents batch", zap.Error(err))
			sendProblemDetail(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	case "application/cloudevents+json":
		// Single event
		var event cloudevents.Event
		if err := decoder.Decode(&event); err != nil {
			logger.ErrorContext(traceCtx, "Failed to parse CloudEvent", zap.Error(err))
			sendProblemDetail(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		events = []*cloudevents.Event{&event}
	default:
		logger.WarnContext(traceCtx, "Unsupported Content-Type for CloudEvents",
			zap.String("content_type", contentType))
		sendProblemDetail(w, http.StatusUnsupportedMediaType, "Unsupported Media Type",
			"CloudEvents endpoint requires Content-Type: application/cloudevents+json or application/cloudevents-batch+json")
		return
	}

	logger.InfoContext(traceCtx, "Parsed CloudEvents", zap.Int("count", len(events)))

	result := s.processCloudEvents(traceCtx, events)

	// Send response using the common response handler
	s.sendEventResponse(w, traceCtx, result)
}

// GetStatsV3 returns aggregated stats for all contexts in a namespace.
// Supports `?view=filtered` to read from the filtered stats table instead of stats_v3.
func (s *Server) GetStatsV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	// Extract namespace from URL path
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	if namespace == "" {
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "namespace is required")
		return
	}

	if !middleware.IsTenantAuthorized(traceCtx, namespace) {
		sendProblemDetail(w, http.StatusForbidden, "Forbidden", "tenant is not authorized")
		return
	}

	// Optional: select an alternate view. The legacy value remains a compatibility alias.
	view := r.URL.Query().Get("view")
	statsView := view
	switch view {
	case defaultStatsView, filteredStatsView:
		// supported
	case legacyFilteredStatsView:
		statsView = filteredStatsView
	default:
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("invalid view param: %q (allowed: filtered)", view))
		return
	}

	if statsView == filteredStatsView && len(s.filteredStatsEventNames) == 0 {
		sendProblemDetail(w, http.StatusNotFound, "Not Found",
			"filtered stats view is not enabled")
		return
	}

	logger.InfoContext(traceCtx, "Retrieving stats for namespace",
		zap.String("namespace", namespace),
		zap.String("view", statsView))

	// Optional: Parse event filter from query params
	eventFilter := r.URL.Query().Get("eventFilter")
	var filterEvents map[string]struct{}
	if eventFilter != "" {
		filterEvents = make(map[string]struct{})
		for _, event := range strings.Split(eventFilter, ",") {
			filterEvents[strings.TrimSpace(event)] = struct{}{}
		}
	}

	// Retrieve stats from the selected table
	var (
		records []data_access.StatsV3Record
		err     error
	)
	switch statsView {
	case filteredStatsView:
		records, err = s.conns.DbHandlerV2.GetFilteredStatsV3(traceCtx, namespace)
	default:
		records, err = s.conns.DbHandlerV2.GetStatsV3(traceCtx, namespace)
	}
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to retrieve stats",
			zap.Error(err),
			zap.String("namespace", namespace),
			zap.String("view", statsView))
		sendProblemDetail(w, http.StatusInternalServerError, "Internal Server Error",
			"Failed to retrieve stats from database")
		return
	}

	// Build the response structure
	response := StatsV3Response{
		Namespace: namespace,
		Summary: StatsSummary{
			TotalContexts: 0,
			ByEvent:       make(map[string]int),
		},
		Contexts: []ContextStats{},
	}

	// Build flat list of contexts and summary
	for _, record := range records {
		// Apply event filter if specified
		if filterEvents != nil {
			if _, ok := filterEvents[record.EventName]; !ok {
				continue
			}
		}

		// Add to flat contexts list
		response.Contexts = append(response.Contexts, ContextStats{
			Context:   record.Context,
			EventName: record.EventName,
			Timestamp: record.Timestamp,
			FirstSeen: record.CreatedAt,
		})

		// Update summary counts
		response.Summary.ByEvent[record.EventName]++
	}

	// Calculate total contexts (after filtering)
	response.Summary.TotalContexts = len(response.Contexts)

	logger.InfoContext(traceCtx, "Successfully retrieved stats",
		zap.String("namespace", namespace),
		zap.Int("total_contexts", response.Summary.TotalContexts))

	sendJSONResponse(w, http.StatusOK, response)
}

// GetEventsV3 handles GET requests to /v3/ledger/namespace/{namespace}/context/{context}/events
// Returns all events for a specific namespace and context, ordered by timestamp descending
func (s *Server) GetEventsV3(w http.ResponseWriter, r *http.Request) {
	traceCtx := r.Context()
	logger := logging.GetLogger(traceCtx)

	// Extract namespace from URL path
	vars := mux.Vars(r)
	namespace := vars["namespace"]

	if namespace == "" {
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request", "namespace is required")
		return
	}

	if !middleware.IsTenantAuthorized(traceCtx, namespace) {
		sendProblemDetail(w, http.StatusForbidden, "Forbidden", "tenant is not authorized")
		return
	}

	// Extract context components from query parameters. resource_id lets callers
	// look up rows keyed by a generic resource identifier (e.g. an ICMSRequest);
	// omitting it yields the same Pod-shaped lookup as before.
	queryParams := r.URL.Query()
	contextV3 := ContextV3{
		InstanceID:         queryParams.Get(contextFieldInstanceID),
		DeploymentID:       queryParams.Get(contextFieldDeploymentID),
		GPUSpecificationID: queryParams.Get(contextFieldGPUSpecificationID),
		ClusterID:          queryParams.Get(contextFieldClusterID),
		ResourceID:         queryParams.Get(contextFieldResourceID),
	}

	// Convert ContextV3 to canonical string
	eventContext, err := eventContextToCanonical(contextV3)
	if err != nil {
		logger.WarnContext(traceCtx, "Invalid context query parameters",
			zap.Error(err))
		sendProblemDetail(w, http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("Invalid context parameters: %s", err.Error()))
		return
	}

	// Context is optional - allow empty string to query events with no context
	logger.InfoContext(traceCtx, "Retrieving events for namespace and context",
		zap.String("namespace", namespace),
		zap.String("context", eventContext),
		zap.Any("context_components", contextV3))

	// Retrieve all events for this namespace+context
	records, err := s.conns.DbHandlerV2.GetEventsV3(traceCtx, namespace, eventContext)
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to retrieve events",
			zap.Error(err),
			zap.String("namespace", namespace),
			zap.String("context", eventContext))
		sendProblemDetail(w, http.StatusInternalServerError, "Internal Server Error",
			"Failed to retrieve events from database")
		return
	}

	// Sort records by timestamp descending (most recent first)
	// Cassandra can't ORDER BY timestamp since it's not a clustering key
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})

	// Build the response by unmarshaling details from each record
	response := EventsV3Response{
		Namespace: namespace,
		Context:   eventContext,
		Events:    make([]EventV3Item, 0, len(records)),
	}

	for _, record := range records {
		// Unmarshal details JSON to EventDetails struct
		var details EventDetails
		// Handle empty or malformed details gracefully (e.g., from old records)
		if len(record.Details) > 0 {
			if err := json.Unmarshal(record.Details, &details); err != nil {
				logger.WarnContext(traceCtx, "Failed to unmarshal event details, using empty details",
					zap.Error(err),
					zap.String("event_name", record.EventName),
					zap.Time("timestamp", record.Timestamp),
					zap.String("details_json", string(record.Details)))
				// Use empty EventDetails struct for malformed data
				details = EventDetails{}
			}
		}

		response.Events = append(response.Events, EventV3Item{
			EventName: record.EventName,
			Source:    record.Source,
			Timestamp: record.Timestamp,
			Details:   details,
			CreatedAt: record.CreatedAt,
			UpdatedAt: record.UpdatedAt,
		})
	}

	logger.InfoContext(traceCtx, "Successfully retrieved events",
		zap.String("namespace", namespace),
		zap.String("context", eventContext),
		zap.Int("event_count", len(response.Events)))

	sendJSONResponse(w, http.StatusOK, response)
}

// sendProblemDetail sends an RFC 9457 problem details response
func sendProblemDetail(w http.ResponseWriter, status int, title, detail string) {
	problem := types.ProblemDetails{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem) // Error ignored: headers already sent
}

// sendJSONResponse sends a JSON response with proper content-type
func sendJSONResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data) // Error ignored: headers already sent
}

// isStatsEnabled checks if an event name should trigger stats_v3 updates
func (s *Server) isStatsEnabled(eventName string) bool {
	// If no filter configured (empty set), enable stats for all events
	if len(s.statsEventNames) == 0 {
		return true
	}

	_, exists := s.statsEventNames[eventName]
	return exists
}

// isFilteredStatsEnabled checks if an event name should update the filtered stats view.
// An empty allowlist disables the view entirely rather than enabling all events.
func (s *Server) isFilteredStatsEnabled(eventName string) bool {
	if len(s.filteredStatsEventNames) == 0 {
		return false
	}
	_, exists := s.filteredStatsEventNames[eventName]
	return exists
}
