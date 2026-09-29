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

package cassandra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/iancoleman/strcase"
	"go.uber.org/zap"

	sq "github.com/Masterminds/squirrel"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
)

type steRecord struct {
	functionVersionId gocql.UUID
	instanceId        string
	event             string
	createdAt         time.Time
	details           json.RawMessage
	eventType         string
	functionId        gocql.UUID
	ncaId             string
	timestamp         time.Time
	archived          bool
}

func (c *CassandraHandler) ListStageTransitionEvents(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string) ([]types.StageTransitionEvent, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildListSTESelect(functionVersionId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "error building list STE select", zap.Error(err))
		return nil, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var events []types.StageTransitionEvent
	var record steRecord

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		events = nil
		iter := c.session.Query(query, args...).WithContext(traceCtx).Iter()
		defer iter.Close()

		for iter.Scan(&record.functionVersionId, &record.instanceId, &record.event, &record.createdAt, &record.details, &record.eventType, &record.functionId, &record.ncaId, &record.timestamp, &record.archived) {
			if !record.archived {
				ste, err := c.steRecordToSTE(traceCtx, record)
				if err != nil {
					logger.ErrorContext(traceCtx, "failed to parse stage transition event", zap.Error(err))
					return fmt.Errorf("failed to parse stage transition event: %v", err)
				}
				events = append(events, ste)
			}
		}

		return iter.Close()
	}, "ListStageTransitionEvents")

	if err != nil {
		logger.ErrorContext(traceCtx, "failed to iterate", zap.Error(err))
		return []types.StageTransitionEvent{}, err
	}

	logger.InfoContext(traceCtx, fmt.Sprintf("num of events: %d", len(events)), zap.String("functionVersionId", functionVersionId.String()), zap.String("instanceId", instanceId))
	if len(events) == 0 {
		return []types.StageTransitionEvent{}, fmt.Errorf("not found")
	} else {
		return events, nil
	}
}

func buildListSTESelect(functionVersionId uuid.UUID, instanceId string) (string, []interface{}, error) {
	columns := []string{"function_version_id", "instance_id", "event", "created_at", "details", "event_Type", "function_id", "nca_id", "timestamp", "archived"}

	queryBuilder := sq.
		Select(columns...).
		From("events").
		Where(sq.Eq{"function_version_id": functionVersionId.String()}).
		Where(sq.Eq{"instance_id": instanceId})
	// Where(sq.Eq{"archived": false})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "error", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) steRecordToSTE(traceCtx context.Context, record steRecord) (types.StageTransitionEvent, error) {
	var functionId, functionVersionId uuid.UUID
	var timestamp time.Time
	var ncaId, instanceId, event, eventType string
	var details json.RawMessage
	var err error
	logger := logging.GetLogger(traceCtx)

	functionId, err = uuid.FromBytes(record.functionId.Bytes())
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to decode function id", zap.Error(err))
		return types.ErrStageTransitionEvent, err
	}
	functionVersionId, err = uuid.FromBytes(record.functionVersionId.Bytes())
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to decode function version id", zap.Error(err))
		return types.ErrStageTransitionEvent, err
	}
	ncaId = record.ncaId
	instanceId = record.instanceId
	event = record.event
	eventType = record.eventType
	timestamp = record.timestamp
	details = record.details

	ste, err := types.NewStageTransitionEvent(ncaId, functionId, functionVersionId, instanceId, event, eventType, timestamp, details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to parse stage transition event record", zap.Error(err))
		return types.ErrStageTransitionEvent, fmt.Errorf("failed to parse stage transition event record: %v", err)
	}
	return ste, nil
}

func (c *CassandraHandler) WriteStageTransitionEvent(traceCtx context.Context, ste types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	applied, err := c.eventsInsert(traceCtx, ste)
	if err != nil {
		return err
	}
	if !applied {
		logger.WarnContext(traceCtx, "event already exists for this instanceId", zap.String("functionVersionId", ste.FunctionVersionId.String()), zap.String("instanceId", ste.InstanceId), zap.String("event", ste.Event))
		return fmt.Errorf("DUPLICATE EVENT: event '%s' already exists for this instanceId: %v", ste.Event, ste.InstanceId)
	}

	err = c.functionVersionInstancesInsert(traceCtx, ste)
	if err != nil {
		return err
	}

	err = c.stateBucketsUpdate(traceCtx, ste)
	if err != nil {
		return err
	}
	logger.InfoContext(traceCtx, "successfully wrote stage transition event", zap.Any("stage transition event", ste))
	return nil
}

func (c *CassandraHandler) stateBucketsUpdate(traceCtx context.Context, ste types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildStateBucketsUpdate(ste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to parse stage transition event record", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to increment counter in cassandra", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully incremented state_buckets counter", zap.String("counter", ste.Event))
		return nil
	}, "stateBucketsUpdate")
}

func buildStateBucketsUpdate(ste types.StageTransitionEvent) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	snakeEvent := strcase.ToSnake(ste.Event)
	expr := fmt.Sprintf("%s + 1", snakeEvent)
	queryBuilder := sq.Update("state_buckets").
		Set(snakeEvent, sq.Expr(expr)).
		Where(sq.Eq{"function_version_id": ste.FunctionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId})

	// Generate the CQL query
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	return query, args, nil
}

func (c *CassandraHandler) eventsInsert(traceCtx context.Context, ste types.StageTransitionEvent) (bool, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildEventsInsert(ste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate insert for stage transition event record", zap.Error(err))
		return false, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var applied bool
	err = c.executeWithSessionRecreation(traceCtx, func() error {
		previous := make(map[string]interface{})
		casApplied, err := c.session.Query(query, args...).WithContext(traceCtx).MapScanCAS(previous)
		applied = casApplied
		if err != nil {
			logger.ErrorContext(traceCtx, "failed to write message to cassandra", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted event", zap.String("event", ste.Event))
		return nil
	}, "eventsInsert")
	return applied, err
}

func buildEventsInsert(ste types.StageTransitionEvent) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at", "archived"}
	values := []interface{}{
		ste.NcaId,
		gocql.UUID(ste.FunctionId),
		gocql.UUID(ste.FunctionVersionId),
		ste.InstanceId,
		gocql.UUID(deploymentId),
		ste.Event,
		ste.EventType,
		ste.Timestamp.UnixMilli(),
		ste.Details,
		time.Now().UnixMilli(),
		false,
	}
	queryBuilder := sq.Insert("events").Columns(columns...).Values(values...).Suffix("IF NOT EXISTS")

	// Generate the CQL query
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	return query, args, nil
}

func (c *CassandraHandler) functionVersionInstancesInsert(traceCtx context.Context, ste types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildFunctionVersionInstancesInsert(ste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build insert statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to write stage transition event", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted into functionVersionInstances",
			zap.String("instanceId", ste.InstanceId),
			zap.String("functionVersionId", ste.FunctionVersionId.String()),
			zap.String("lastEvent", ste.Event),
			zap.String("lastEventTimestamp", ste.Timestamp.String()))
		return nil
	}, "functionVersionInstancesInsert")
}

func buildFunctionVersionInstancesInsert(ste types.StageTransitionEvent) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	columns := []string{"function_version_id", "deployment_id", "instance_id", "last_event", "last_event_details", "last_event_timestamp", "archived"}
	values := []interface{}{gocql.UUID(ste.FunctionVersionId), gocql.UUID(deploymentId), ste.InstanceId, ste.Event, ste.Details, ste.Timestamp.UnixMilli(), false}
	if ste.Event == "building" {
		columns = append(columns, "deploy_start_timestamp")
		values = append(values, ste.Timestamp.UnixMilli())
	}

	instanceType, err := extractInstanceType(ste.Details)
	if err != nil {
		return "error", nil, err
	}
	if instanceType != "" {
		columns = append(columns, "instance_type")
		values = append(values, instanceType)
	}

	queryBuilder := sq.Insert("function_version_instances").Columns(columns...).Values(values...)

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "error", nil, err
	}
	return query, args, nil
}

func extractInstanceType(details json.RawMessage) (string, error) {
	if len(details) == 0 {
		return "", nil
	}

	var detailsMap map[string]interface{}
	if err := json.Unmarshal(details, &detailsMap); err != nil {
		return "", fmt.Errorf("failed to parse event details: %w", err)
	}

	value, exists := detailsMap["instanceType"]
	if !exists || value == nil {
		return "", nil
	}

	instanceType, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("event details instanceType must be a string")
	}
	return instanceType, nil
}

func (c *CassandraHandler) ListInstances(traceCtx context.Context, functionVersionId uuid.UUID) ([]types.Instance, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	var instances []types.Instance
	query, args, err := buildListInstancesSelect(functionVersionId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build list instances statement", zap.Error(err))
		return []types.Instance{types.ErrInstance}, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	type InstanceRecord struct {
		id                   string
		lastEvent            string
		lastEventDetails     json.RawMessage
		lastEventTimestamp   time.Time
		deployStartTimestamp time.Time
		instanceType         string
		archived             bool
	}
	var record InstanceRecord

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		instances = nil
		iter := c.session.Query(query, args...).WithContext(traceCtx).Iter()
		defer iter.Close()

		for iter.Scan(&record.id, &record.lastEvent, &record.lastEventDetails, &record.lastEventTimestamp, &record.deployStartTimestamp, &record.instanceType, &record.archived) {
			logger.DebugContext(
				traceCtx,
				"received instance id",
				zap.String("instanceId", record.id),
				zap.String("lastEvent", record.lastEvent),
				zap.String("lastEventTimestamp", record.lastEventTimestamp.String()))
			if !record.archived {
				codexInstance := types.NewInstance(record.id, record.lastEvent, record.lastEventDetails, record.lastEventTimestamp, record.deployStartTimestamp, record.instanceType)
				instances = append(instances, codexInstance)
			}
		}

		return iter.Close()
	}, "ListInstances")

	logger.InfoContext(traceCtx, fmt.Sprintf("num of instances: %d", len(instances)), zap.String("functionVersionId", functionVersionId.String()))
	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to iterate", zap.Error(err))
		return []types.Instance{types.ErrInstance}, err
	}

	if len(instances) == 0 {
		return instances, fmt.Errorf("not found")
	}
	return instances, nil
}

func buildListInstancesSelect(functionVersionId uuid.UUID) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	columns := []string{"instance_id", "last_event", "last_event_details", "last_event_timestamp", "deploy_start_timestamp", "instance_type", "archived"}

	queryBuilder := sq.Select(columns...).From("function_version_instances").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "error", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) ArchiveInstanceStageTransitionEvents(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	stes, err := c.ListStageTransitionEvents(traceCtx, functionVersionId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to list stage transition events", zap.Error(err))
		return err
	}

	// WaitGroup to wait for all goroutines
	var wg sync.WaitGroup

	// Channel for errors
	errCh := make(chan error, len(stes))

	for _, ste := range stes {
		wg.Add(1)
		eventCopy := ste.Event // Capture a copy of `ste.Event`
		go func(event string) {
			defer wg.Done()
			defer func() {
				if panicErr := recover(); panicErr != nil { // deferred panic handler in goroutines
					errCh <- fmt.Errorf("panic: %v", panicErr)
				}
			}()

			err := c.archiveEvents(traceCtx, functionVersionId, instanceId, event)
			if err != nil {
				errCh <- err
			}

		}(eventCopy)

	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errCh)

	// Handle errors
	errCount := 0
	for err = range errCh {
		logger.ErrorContext(traceCtx, "failed to archive instance ste records", zap.Error(err))
		errCount++
	}
	if errCount > 0 {
		return fmt.Errorf("%d errors archiving instance ste records", errCount)
	}
	err = c.archiveFunctionVersionInstances(traceCtx, functionVersionId, instanceId)
	if err != nil {
		return err
	}
	logger.InfoContext(traceCtx, "successfully archived instance ste records", zap.String("functionVersionId", functionVersionId.String()), zap.String("instanceId", instanceId))
	return nil
}

func (c *CassandraHandler) archiveEvents(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string, event string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildArchiveEventsUpdate(functionVersionId, instanceId, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate event archive update statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		return c.session.Query(query, args...).WithContext(traceCtx).Exec()
	}, "archiveEvents")
}

func buildArchiveEventsUpdate(functionVersionId uuid.UUID, instanceId string, event string) (string, []interface{}, error) {
	queryBuilder := sq.Update("events").
		Set("archived", true).
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"instance_id": instanceId}).
		Where(sq.Eq{"event": event})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "error", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) archiveFunctionVersionInstances(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildArchiveFunctionVersionInstancesUpdate(functionVersionId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate event archive update statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		return c.session.Query(query, args...).WithContext(traceCtx).Exec()
	}, "archiveFunctionVersionInstances")
}

func buildArchiveFunctionVersionInstancesUpdate(functionVersionId uuid.UUID, instanceId string) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	queryBuilder := sq.Update("function_version_instances").
		Set("archived", true).
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Where(sq.Eq{"instance_id": instanceId})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "error", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) ReadDeploymentStats(traceCtx context.Context, functionVersionId uuid.UUID) (types.DeploymentStats, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	type statRecord struct {
		functionVersionId          gocql.UUID
		deploymentId               gocql.UUID
		pending                    int
		pendingError               int
		building                   int
		buildingError              int
		downloadingModel           int
		downloadingModelError      int
		downloadingContainer       int
		downloadingContainerError  int
		initializingContainer      int
		initializingContainerError int
		ready                      int
		// active                     int (not in database)
		requestingTermination int
		destroyed             int
	}
	var record statRecord

	query, args, err := buildReadDeploymentStatsSelect(functionVersionId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate deployment stats select statement", zap.Error(err))
		return types.ErrDeploymentStats, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		return c.session.Query(query, args...).WithContext(traceCtx).Scan(
			&record.functionVersionId,
			&record.deploymentId,
			&record.pending,
			&record.pendingError,
			&record.building,
			&record.buildingError,
			&record.downloadingModel,
			&record.downloadingModelError,
			&record.downloadingContainer,
			&record.downloadingContainerError,
			&record.initializingContainer,
			&record.initializingContainerError,
			&record.ready,
			&record.requestingTermination,
			&record.destroyed,
		)
	}, "ReadDeploymentStats")

	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			logger.InfoContext(traceCtx, "message not found in Cassandra")
			return types.ErrDeploymentStats, fmt.Errorf("stats not found")
		} else {
			logger.ErrorContext(traceCtx, "failed to read message from database", zap.Error(err))
			return types.ErrDeploymentStats, fmt.Errorf("query failed: %v", err)
		}
	}
	recordFnVerId, err := uuid.FromBytes(record.functionVersionId.Bytes())
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to decode function version id", zap.Error(err))
		return types.ErrDeploymentStats, fmt.Errorf("failed to decode function version id: %v", err)
	}
	stats := types.NewDeploymentStats(
		recordFnVerId,
		record.pending,
		record.pendingError,
		record.building,
		record.buildingError,
		record.downloadingModel,
		record.downloadingModelError,
		record.downloadingContainer,
		record.downloadingContainerError,
		record.initializingContainer,
		record.initializingContainerError,
		record.ready,
		record.requestingTermination,
		record.destroyed)

	logger.InfoContext(traceCtx, "deployment stats", zap.Any("stats", stats))
	return stats, nil
}

func buildReadDeploymentStatsSelect(functionVersionId uuid.UUID) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	// columns := []string{"function_version_id", "deployment_id", "pending", "pending_error", "building", "building_error", "downloading_model", "downloading_model_error", "downloading_container", "downloading_container_error", "initializing_container", "initializing_container_error", "ready", "active", "requesting_termination", "destroyed"}
	columns := []string{"function_version_id", "deployment_id", "pending", "pending_error", "building", "building_error", "downloading_model", "downloading_model_error", "downloading_container", "downloading_container_error", "initializing_container", "initializing_container_error", "ready", "requesting_termination", "destroyed"}
	queryBuilder := sq.Select(columns...).From("state_buckets").Where(sq.Eq{"function_version_id": functionVersionId}).Where(sq.Eq{"deployment_id": deploymentId})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) GetInstanceEvent(traceCtx context.Context, functionVersionId uuid.UUID, instanceId string, event codex.Event) (types.StageTransitionEvent, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildGetInstanceEventSelect(functionVersionId, instanceId, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate instance event select statement", zap.Error(err))
		return types.ErrStageTransitionEvent, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var record steRecord

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		queryErr := c.session.Query(query, args...).WithContext(traceCtx).Scan(&record.functionVersionId, &record.instanceId, &record.event, &record.details, &record.eventType, &record.functionId, &record.ncaId, &record.timestamp)
		return queryErr
	}, "GetInstanceEvent")

	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			logger.InfoContext(traceCtx, "stage_transition_event not found in Cassandra")
			return types.ErrStageTransitionEvent, fmt.Errorf("stage_transition_event not found")
		} else {
			logger.ErrorContext(traceCtx, "failed to read message from database", zap.Error(err))
			return types.ErrStageTransitionEvent, fmt.Errorf("query failed: %v", err)
		}
	}

	ste, err := c.steRecordToSTE(traceCtx, record)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to decode stage transition event", zap.Error(err))
		return types.ErrStageTransitionEvent, fmt.Errorf("failed to decode stage transition event: %v", err)
	}
	return ste, nil
}

func buildGetInstanceEventSelect(functionVersionId uuid.UUID, instanceId string, event codex.Event) (string, []interface{}, error) {
	columns := []string{"function_version_id", "instance_id", "event", "details", "event_type", "function_id", "nca_id", "timestamp"}
	queryBuilder := sq.Select(columns...).From("events").Where(sq.Eq{"function_version_id": functionVersionId}).Where(sq.Eq{"instance_id": instanceId}).Where(sq.Eq{"event": event.Name})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

func (c *CassandraHandler) Close() error {
	c.session.Close()
	return nil
}

// IndexByTimestamp inserts a record into lookup_by_timestamp table
func (c *CassandraHandler) IndexByTimestamp(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	time_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}
	err = c.timeInsert(traceCtx, time_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by timestamp", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by timestamp", zap.String("timestamp", event.Timestamp.String()))
	return nil
}

// IndexByFunctionId inserts a record into lookup_by_function_id table
func (c *CassandraHandler) IndexByFunctionId(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	fnid_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}

	err = c.functionIdInsert(traceCtx, fnid_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by function id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by function id", zap.String("functionId", event.FunctionId.String()))
	return nil
}

// IndexByFunctionVersionId inserts a record into lookup_by_function_version_id table
func (c *CassandraHandler) IndexByFunctionVersionId(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	fnverid_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}

	err = c.functionVersionIdInsert(traceCtx, fnverid_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by function version id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by function version id", zap.String("functionVersionId", event.FunctionVersionId.String()))
	return nil
}

// IndexByInstanceId inserts a record into lookup_by_instance_id table
func (c *CassandraHandler) IndexByInstanceId(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	instanceid_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}

	err = c.instanceIdInsert(traceCtx, instanceid_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by instance id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by instance id", zap.String("instanceId", event.InstanceId))
	return nil
}

// IndexByDeploymentId inserts a record into lookup_by_deployment_id table
func (c *CassandraHandler) IndexByDeploymentId(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	deploymentid_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}

	err = c.deploymentIdInsert(traceCtx, deploymentid_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by deployment id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by deployment id")
	return nil
}

// IndexByNcaId inserts a record into lookup_by_nca_id table for V1
func (c *CassandraHandler) IndexByNcaId(traceCtx context.Context, event types.StageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	ncaid_event, err := types.NewDeploymentStageTransitionEvent(event.NcaId, event.FunctionId, event.FunctionVersionId, uuid.Nil, event.InstanceId, event.Event, event.EventType, event.Timestamp, event.Details)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build deployment stage transition event", zap.Error(err))
		return err
	}

	err = c.ncaIdInsert(traceCtx, ncaid_event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by nca id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by nca id", zap.String("ncaId", event.NcaId))
	return nil
}

// ListInstancesPaginated retrieves instances with pagination support
func (c *CassandraHandler) ListInstancesPaginated(traceCtx context.Context, functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	// Request one more than the limit to determine if there are more pages
	queryLimit := paginationParams.Limit + 1

	var instances []types.Instance
	query, args, err := buildListInstancesSelectPaginated(functionVersionId, data_access.PaginationParams{
		Limit:     queryLimit,
		PageToken: paginationParams.PageToken,
	})
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build paginated list instances statement", zap.Error(err))
		return data_access.PaginatedInstancesResponse{}, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	type InstanceRecord struct {
		id                   string
		lastEvent            string
		lastEventDetails     json.RawMessage
		lastEventTimestamp   time.Time
		deployStartTimestamp time.Time
		instanceType         string
		archived             bool
	}
	var record InstanceRecord

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		instances = nil
		iter := c.session.Query(query, args...).WithContext(traceCtx).Iter()
		defer iter.Close()

		for iter.Scan(&record.id, &record.lastEvent, &record.lastEventDetails, &record.lastEventTimestamp, &record.deployStartTimestamp, &record.instanceType, &record.archived) {
			if !record.archived {
				instance := types.NewInstance(record.id, record.lastEvent, record.lastEventDetails, record.lastEventTimestamp, record.deployStartTimestamp, record.instanceType)
				instances = append(instances, instance)
			}
		}

		return iter.Close()
	}, "ListInstancesPaginated")

	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to iterate paginated instances", zap.Error(err))
		return data_access.PaginatedInstancesResponse{}, err
	}

	// Determine if there are more results and adjust the response
	hasMore := len(instances) > paginationParams.Limit
	if hasMore {
		// Remove the extra instance we fetched for pagination check
		instances = instances[:paginationParams.Limit]
	}

	nextPageToken := ""
	if hasMore && len(instances) > 0 {
		nextPageToken = instances[len(instances)-1].InstanceId
	}

	logger.InfoContext(traceCtx, fmt.Sprintf("num of paginated instances: %d", len(instances)),
		zap.String("functionVersionId", functionVersionId.String()),
		zap.Bool("hasMore", hasMore))

	return data_access.PaginatedInstancesResponse{
		Instances: instances,
		Pagination: data_access.PaginationMeta{
			PageSize:      len(instances),
			NextPageToken: nextPageToken,
			HasMore:       hasMore,
		},
	}, nil
}

// buildListInstancesSelectPaginated builds a paginated query for instances
func buildListInstancesSelectPaginated(functionVersionId uuid.UUID, paginationParams data_access.PaginationParams) (string, []interface{}, error) {
	deploymentId := uuid.Nil
	columns := []string{"instance_id", "last_event", "last_event_details", "last_event_timestamp", "deploy_start_timestamp", "instance_type", "archived"}

	queryBuilder := sq.Select(columns...).From("function_version_instances").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Limit(uint64(paginationParams.Limit))

	// Add pagination token condition if provided
	if paginationParams.PageToken != "" {
		queryBuilder = queryBuilder.Where(sq.Gt{"instance_id": paginationParams.PageToken})
	}

	// Note: Removed ORDER BY clause - Cassandra clustering key already provides natural ordering

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}
