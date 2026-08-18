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
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/iancoleman/strcase"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	sq "github.com/Masterminds/squirrel"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
)

const (
	statsV3Table         = "stats_v3"
	filteredStatsV3Table = "stats_v3_ngc"
)

// --- V2 Methods ---

func (c *CassandraHandler) WriteDeploymentStageTransitionEvent(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	applied, err := c.eventsInsertV2(traceCtx, dste)
	if err != nil {
		return err
	}
	if !applied {
		logger.WarnContext(traceCtx, "deployment event already exists for this deployment instanceId",
			zap.String("functionVersionId", dste.FunctionVersionId.String()),
			zap.String("deploymentId", dste.DeploymentId.String()),
			zap.String("instanceId", dste.InstanceId),
			zap.String("event", dste.Event))
		return fmt.Errorf("DUPLICATE EVENT: event '%s' already exists for this deployment instanceId: %v/%v", dste.Event, dste.DeploymentId, dste.InstanceId)
	}

	err = c.functionVersionInstancesInsertV2(traceCtx, dste)
	if err != nil {
		return err
	}

	err = c.stateBucketsUpdateV2(traceCtx, dste)
	if err != nil {
		return err
	}

	logger.InfoContext(traceCtx, "successfully wrote deployment stage transition event", zap.String("event", dste.Event))
	return nil
}

// eventsInsertV2 handles inserting V2 events with deploymentId
func (c *CassandraHandler) eventsInsertV2(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) (bool, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildDeploymentEventsInsert(dste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate insert for deployment stage transition event record", zap.Error(err))
		return false, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var applied bool
	err = c.executeWithSessionRecreation(traceCtx, func() error {
		previous := make(map[string]interface{})
		casApplied, err := c.session.Query(query, args...).WithContext(traceCtx).MapScanCAS(previous)
		applied = casApplied
		if err != nil {
			logger.ErrorContext(traceCtx, "failed to write deployment message to cassandra", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted deployment event", zap.String("event", dste.Event))
		return nil
	}, "eventsInsertV2")
	return applied, err
}

func buildDeploymentEventsInsert(dste types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at", "archived"}
	values := []interface{}{
		dste.NcaId,
		gocql.UUID(dste.FunctionId),
		gocql.UUID(dste.FunctionVersionId),
		dste.InstanceId,
		gocql.UUID(dste.DeploymentId), // Include deploymentId
		dste.Event,
		dste.EventType,
		dste.Timestamp.UnixMilli(),
		dste.Details,
		time.Now().UnixMilli(),
		false,
	}
	queryBuilder := sq.Insert("events_v2").Columns(columns...).Values(values...).Suffix("IF NOT EXISTS")

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	return query, args, nil
}

// functionVersionInstancesInsertV2 handles inserting/updating V2 instance records with deploymentId
func (c *CassandraHandler) functionVersionInstancesInsertV2(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildFunctionVersionInstancesInsertV2(dste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build v2 insert statement for functionVersionInstances", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to write v2 stage transition event to functionVersionInstances", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted/updated v2 functionVersionInstances",
			zap.String("instanceId", dste.InstanceId),
			zap.String("functionVersionId", dste.FunctionVersionId.String()),
			zap.String("deploymentId", dste.DeploymentId.String()),
			zap.String("lastEvent", dste.Event),
			zap.String("lastEventTimestamp", dste.Timestamp.String()))
		return nil
	}, "functionVersionInstancesInsertV2")
}

func buildFunctionVersionInstancesInsertV2(dste types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"function_version_id", "deployment_id", "instance_id", "last_event", "last_event_details", "last_event_timestamp", "archived"}
	values := []interface{}{gocql.UUID(dste.FunctionVersionId), gocql.UUID(dste.DeploymentId), dste.InstanceId, dste.Event, dste.Details, dste.Timestamp.UnixMilli(), false}

	if dste.Event == "building" { // Assuming 'building' still signifies the start
		columns = append(columns, "deploy_start_timestamp")
		values = append(values, dste.Timestamp.UnixMilli())
	}
	instanceType, err := extractInstanceType(dste.Details)
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
		return "", nil, err
	}
	return query, args, nil
}

// stateBucketsUpdateV2 handles updating V2 state counters with deploymentId
func (c *CassandraHandler) stateBucketsUpdateV2(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildStateBucketsUpdateV2(dste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build v2 state_buckets update statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to increment v2 counter in cassandra state_buckets", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully incremented v2 state_buckets counter", zap.String("counter", dste.Event))
		return nil
	}, "stateBucketsUpdateV2")
}

func buildStateBucketsUpdateV2(dste types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	snakeEvent := strcase.ToSnake(dste.Event)
	expr := fmt.Sprintf("%s + 1", snakeEvent)
	queryBuilder := sq.Update("state_buckets").
		Set(snakeEvent, sq.Expr(expr)).
		Where(sq.Eq{"function_version_id": dste.FunctionVersionId}).
		Where(sq.Eq{"deployment_id": dste.DeploymentId})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	return query, args, nil
}

// --- End V2 Write Methods ---

// ListDeploymentStageTransitionEvents retrieves events for a specific deployment instance.
func (c *CassandraHandler) ListDeploymentStageTransitionEvents(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string) ([]types.StageTransitionEvent, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildListDeploymentSTESelect(functionVersionId, deploymentId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "error building list deployment STE select", zap.Error(err))
		return nil, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var events []types.StageTransitionEvent
	var record steRecord // Reusing steRecord from V1

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		events = nil
		iter := c.session.Query(query, args...).WithContext(traceCtx).Iter()
		defer iter.Close()

		for iter.Scan(&record.functionVersionId, &record.instanceId, &record.event, &record.createdAt, &record.details, &record.eventType, &record.functionId, &record.ncaId, &record.timestamp, &record.archived) {
			if !record.archived {
				// Using V1 converter, assuming returned events don't need deploymentId explicitly
				ste, err := c.steRecordToSTE(traceCtx, record)
				if err != nil {
					logger.ErrorContext(traceCtx, "failed to parse deployment stage transition event", zap.Error(err))
					// Decide whether to skip or fail. Skipping for now.
					continue
				}
				events = append(events, ste)
			}
		}

		return iter.Close()
	}, "ListDeploymentStageTransitionEvents")

	if err != nil {
		logger.ErrorContext(traceCtx, "failed to iterate deployment events", zap.Error(err))
		return nil, err
	}

	logger.InfoContext(traceCtx, fmt.Sprintf("num of deployment events: %d", len(events)),
		zap.String("functionVersionId", functionVersionId.String()),
		zap.String("deploymentId", deploymentId.String()),
		zap.String("instanceId", instanceId))

	if len(events) == 0 {
		return nil, fmt.Errorf("not found")
	}
	return events, nil
}

func buildListDeploymentSTESelect(functionVersionId, deploymentId uuid.UUID, instanceId string) (string, []interface{}, error) {
	columns := []string{"function_version_id", "instance_id", "event", "created_at", "details", "event_type", "function_id", "nca_id", "timestamp", "archived"}

	queryBuilder := sq.
		Select(columns...).
		From("events_v2").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Where(sq.Eq{"instance_id": instanceId})
	// Where(sq.Eq{"archived": false}) // Removed archived filter as V1 did

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// ListDeploymentInstances retrieves instances for a specific deployment.
func (c *CassandraHandler) ListDeploymentInstances(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID) ([]types.Instance, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	var instances []types.Instance
	query, args, err := buildListDeploymentInstancesSelect(functionVersionId, deploymentId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build list deployment instances statement", zap.Error(err))
		return nil, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	// Reusing V1 InstanceRecord struct definition
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
	}, "ListDeploymentInstances")

	logger.InfoContext(traceCtx, fmt.Sprintf("num of deployment instances: %d", len(instances)),
		zap.String("functionVersionId", functionVersionId.String()),
		zap.String("deploymentId", deploymentId.String()))

	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to iterate deployment instances", zap.Error(err))
		return nil, err
	}

	if len(instances) == 0 {
		// Return empty slice and nil error if not found, consistent with V1 ListInstances behavior
		return instances, fmt.Errorf("not found")
	}
	return instances, nil
}

func buildListDeploymentInstancesSelect(functionVersionId, deploymentId uuid.UUID) (string, []interface{}, error) {
	columns := []string{"instance_id", "last_event", "last_event_details", "last_event_timestamp", "deploy_start_timestamp", "instance_type", "archived"}

	queryBuilder := sq.Select(columns...).
		From("function_version_instances").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId})
	// Where(sq.Eq{"archived": false}) // Removed archived filter as V1 did

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// GetDeploymentInstanceEvent retrieves a specific event for a deployment instance.
func (c *CassandraHandler) GetDeploymentInstanceEvent(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string, event codex.Event) (types.StageTransitionEvent, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildGetDeploymentInstanceEventSelect(functionVersionId, deploymentId, instanceId, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate deployment instance event select statement", zap.Error(err))
		return types.ErrStageTransitionEvent, err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	var record steRecord // Reusing V1 steRecord

	err = c.executeWithSessionRecreation(traceCtx, func() error {
		queryErr := c.session.Query(query, args...).WithContext(traceCtx).Scan(&record.functionVersionId, &record.instanceId, &record.event, &record.details, &record.eventType, &record.functionId, &record.ncaId, &record.timestamp)
		return queryErr
	}, "GetDeploymentInstanceEvent")
	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			logger.InfoContext(traceCtx, "deployment stage_transition_event not found in Cassandra")
			return types.ErrStageTransitionEvent, fmt.Errorf("deployment stage_transition_event not found")
		} else {
			logger.ErrorContext(traceCtx, "failed to read deployment message from database", zap.Error(err))
			return types.ErrStageTransitionEvent, fmt.Errorf("query failed: %v", err)
		}
	}

	// Using V1 converter, assuming returned event doesn't need deploymentId explicitly
	ste, err := c.steRecordToSTE(traceCtx, record)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to decode deployment stage transition event", zap.Error(err))
		return types.ErrStageTransitionEvent, fmt.Errorf("failed to decode deployment stage transition event: %v", err)
	}
	return ste, nil
}

func buildGetDeploymentInstanceEventSelect(functionVersionId, deploymentId uuid.UUID, instanceId string, event codex.Event) (string, []interface{}, error) {
	columns := []string{"function_version_id", "instance_id", "event", "details", "event_type", "function_id", "nca_id", "timestamp"}
	queryBuilder := sq.Select(columns...).
		From("events_v2").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Where(sq.Eq{"instance_id": instanceId}).
		Where(sq.Eq{"event": event.Name})

	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// ArchiveDeploymentInstanceStageTransitionEvents archives events for a specific deployment instance.
func (c *CassandraHandler) ArchiveDeploymentInstanceStageTransitionEvents(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	// First, list the events for this specific deployment instance
	stes, err := c.ListDeploymentStageTransitionEvents(traceCtx, functionVersionId, deploymentId, instanceId)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logger.InfoContext(traceCtx, "no deployment events found to archive for instance",
				zap.String("functionVersionId", functionVersionId.String()),
				zap.String("deploymentId", deploymentId.String()),
				zap.String("instanceId", instanceId))
			return nil // Nothing to archive, return success
		}
		logger.ErrorContext(traceCtx, "failed to list deployment stage transition events for archival", zap.Error(err))
		return err
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(stes))

	for _, ste := range stes {
		wg.Add(1)
		go func(eventToArchive string) {
			defer wg.Done()
			defer func() {
				if panicErr := recover(); panicErr != nil {
					errCh <- fmt.Errorf("panic archiving deployment event: %v", panicErr)
				}
			}()

			err := c.archiveDeploymentEvents(traceCtx, functionVersionId, deploymentId, instanceId, eventToArchive)
			if err != nil {
				errCh <- err
			}
		}(ste.Event) // Pass event name
	}

	wg.Wait()
	close(errCh)

	errCount := 0
	for err := range errCh {
		logger.ErrorContext(traceCtx, "failed to archive one or more deployment instance events", zap.Error(err))
		errCount++
	}
	if errCount > 0 {
		return fmt.Errorf("%d errors archiving deployment instance events", errCount)
	}

	// Now archive the instance record itself
	err = c.archiveDeploymentFunctionVersionInstance(traceCtx, functionVersionId, deploymentId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to archive deployment function version instance record", zap.Error(err))
		return err
	}

	logger.InfoContext(traceCtx, "successfully archived deployment instance events and record",
		zap.String("functionVersionId", functionVersionId.String()),
		zap.String("deploymentId", deploymentId.String()),
		zap.String("instanceId", instanceId))
	return nil
}

// archiveDeploymentEvents archives a single event record for a deployment.
func (c *CassandraHandler) archiveDeploymentEvents(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string, event string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildArchiveDeploymentEventsUpdate(functionVersionId, deploymentId, instanceId, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate deployment event archive update statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		err := c.session.Query(query, args...).WithContext(traceCtx).Exec()
		if err != nil {
			logger.ErrorContext(traceCtx, "failed executing deployment event archive update", zap.Error(err))
		}
		return err // Return error regardless of logging
	}, "archiveDeploymentEvents")
}

func buildArchiveDeploymentEventsUpdate(functionVersionId, deploymentId uuid.UUID, instanceId string, event string) (string, []interface{}, error) {
	queryBuilder := sq.Update("events_v2").
		Set("archived", true).
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Where(sq.Eq{"instance_id": instanceId}).
		Where(sq.Eq{"event": event})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// archiveDeploymentFunctionVersionInstance archives the instance record itself for a deployment.
func (c *CassandraHandler) archiveDeploymentFunctionVersionInstance(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, instanceId string) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildArchiveDeploymentFunctionVersionInstanceUpdate(functionVersionId, deploymentId, instanceId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate deployment instance archive update statement", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		err := c.session.Query(query, args...).WithContext(traceCtx).Exec()
		if err != nil {
			logger.ErrorContext(traceCtx, "failed executing deployment instance archive update", zap.Error(err))
		}
		return err
	}, "archiveDeploymentFunctionVersionInstance")
}

func buildArchiveDeploymentFunctionVersionInstanceUpdate(functionVersionId, deploymentId uuid.UUID, instanceId string) (string, []interface{}, error) {
	queryBuilder := sq.Update("function_version_instances").
		Set("archived", true).
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId}).
		Where(sq.Eq{"instance_id": instanceId})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// ReadDeploymentDeploymentStats retrieves deployment-specific stats.
func (c *CassandraHandler) ReadDeploymentDeploymentStats(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID) (types.DeploymentStats, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	// Reusing V1 statRecord struct
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

	query, args, err := buildReadDeploymentDeploymentStatsSelect(functionVersionId, deploymentId)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate deployment stats select statement for deployment", zap.Error(err))
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
			// &record.active,
			&record.requestingTermination,
			&record.destroyed,
		)
	}, "ReadDeploymentDeploymentStats")

	if err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			logger.InfoContext(traceCtx, "deployment stats not found in Cassandra for deployment")
			return types.ErrDeploymentStats, fmt.Errorf("deployment stats not found")
		} else {
			logger.ErrorContext(traceCtx, "failed to read deployment stats from database for deployment", zap.Error(err))
			return types.ErrDeploymentStats, fmt.Errorf("query failed: %v", err)
		}
	}
	// No need to decode functionVersionId again if it matches the input
	// recordFnVerId, err := uuid.FromBytes(record.functionVersionId.Bytes())
	// if err != nil {
	// 	logger.ErrorContext(traceCtx, "failed to decode function version id", zap.Error(err))
	// 	return types.ErrDeploymentStats, fmt.Errorf("failed to decode function version id: %v", err)
	// }
	stats := types.NewDeploymentStats(
		functionVersionId, // Use input functionVersionId directly
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

	logger.InfoContext(traceCtx, "deployment stats for deployment", zap.Any("stats", stats))
	return stats, nil
}

func buildReadDeploymentDeploymentStatsSelect(functionVersionId, deploymentId uuid.UUID) (string, []interface{}, error) {
	// columns := []string{"function_version_id", "deployment_id", "pending", "pending_error", "building", "building_error", "downloading_model", "downloading_model_error", "downloading_container", "downloading_container_error", "initializing_container", "initializing_container_error", "ready", "active", "requesting_termination", "destroyed"}
	columns := []string{"function_version_id", "deployment_id", "pending", "pending_error", "building", "building_error", "downloading_model", "downloading_model_error", "downloading_container", "downloading_container_error", "initializing_container", "initializing_container_error", "ready", "requesting_termination", "destroyed"}
	queryBuilder := sq.Select(columns...).
		From("state_buckets").
		Where(sq.Eq{"function_version_id": functionVersionId}).
		Where(sq.Eq{"deployment_id": deploymentId})
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// --- End V2 Read/List/Get/Archive Methods ---

// IndexByDeploymentTimestamp inserts a record into lookup_by_timestamp table
func (c *CassandraHandler) IndexByDeploymentTimestamp(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.timeInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index by timestamp", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed by timestamp", zap.String("timestamp", event.Timestamp.String()))
	return nil
}

// IndexByDeploymentFunctionId inserts a record into lookup_by_function_id table for V2
func (c *CassandraHandler) IndexByDeploymentFunctionId(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.functionIdInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index deployment by function id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed deployment by function id", zap.String("functionId", event.FunctionId.String()))
	return nil
}

// IndexByDeploymentFunctionVersionId inserts a record into lookup_by_function_version_id table for V2
func (c *CassandraHandler) IndexByDeploymentFunctionVersionId(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.functionVersionIdInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index deployment by function version id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed deployment by function version id", zap.String("functionVersionId", event.FunctionVersionId.String()))
	return nil
}

// IndexByDeploymentInstanceId inserts a record into lookup_by_instance_id table for V2
func (c *CassandraHandler) IndexByDeploymentInstanceId(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.instanceIdInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index deployment by instance id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed deployment by instance id", zap.String("instanceId", event.InstanceId))
	return nil
}

// IndexByDeploymentDeploymentId inserts a record into lookup_by_deployment_id table for V2
func (c *CassandraHandler) IndexByDeploymentDeploymentId(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.deploymentIdInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index deployment by deployment id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed deployment by deployment id", zap.String("deploymentId", event.DeploymentId.String()))
	return nil
}

// IndexByDeploymentNcaId inserts a record into lookup_by_nca_id table for V2
func (c *CassandraHandler) IndexByDeploymentNcaId(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	err := c.ncaIdInsert(traceCtx, event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to index deployment by nca id", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "successfully indexed deployment by nca id", zap.String("ncaId", event.NcaId))
	return nil
}

// ListDeploymentInstancesPaginated retrieves deployment instances with pagination support
func (c *CassandraHandler) ListDeploymentInstancesPaginated(traceCtx context.Context, functionVersionId, deploymentId uuid.UUID, paginationParams data_access.PaginationParams) (data_access.PaginatedInstancesResponse, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	// Request one more than the limit to determine if there are more pages
	queryLimit := paginationParams.Limit + 1

	var instances []types.Instance
	query, args, err := buildListDeploymentInstancesSelectPaginated(functionVersionId, deploymentId, data_access.PaginationParams{
		Limit:     queryLimit,
		PageToken: paginationParams.PageToken,
	})
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build paginated list deployment instances statement", zap.Error(err))
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
	}, "ListDeploymentInstancesPaginated")

	if err != nil {
		logger.ErrorContext(traceCtx, "Failed to iterate paginated deployment instances", zap.Error(err))
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

	logger.InfoContext(traceCtx, fmt.Sprintf("num of paginated deployment instances: %d", len(instances)),
		zap.String("functionVersionId", functionVersionId.String()),
		zap.String("deploymentId", deploymentId.String()),
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

// buildListDeploymentInstancesSelectPaginated builds a paginated query for deployment instances
func buildListDeploymentInstancesSelectPaginated(functionVersionId, deploymentId uuid.UUID, paginationParams data_access.PaginationParams) (string, []interface{}, error) {
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

// --- Failed CloudEvents Methods ---

// StoreFailedCloudEvent stores a CloudEvent that failed to be sent
func (c *CassandraHandler) StoreFailedCloudEvent(ctx context.Context, eventType string, eventData []byte) error {
	// Tracing handled by external library
	logger := logging.GetLogger(ctx)

	id := gocql.TimeUUID()
	query := `INSERT INTO failed_cloudevents (id, event_type, event_data, retry_count, last_retry_timestamp, created_at) 
			  VALUES (?, ?, ?, ?, ?, ?)`

	err := c.executeWithSessionRecreation(ctx, func() error {
		return c.session.Query(query, id, eventType, string(eventData), 0, time.Now(), time.Now()).
			WithContext(ctx).Exec()
	}, "StoreFailedCloudEvent")

	if err != nil {
		logger.ErrorContext(ctx, "failed to store failed CloudEvent", zap.Error(err))
		return err
	}

	logger.DebugContext(ctx, "stored failed CloudEvent", zap.String("id", id.String()), zap.String("type", eventType))
	return nil
}

// StreamFailedCloudEvents streams failed CloudEvents for retry via iterator
func (c *CassandraHandler) StreamFailedCloudEvents(ctx context.Context, limit int) iter.Seq[data_access.FailedCloudEvent] {

	return func(yield func(data_access.FailedCloudEvent) bool) {
		logger := logging.GetLogger(ctx)

		// Start with an empty last ID to get the first page
		var lastID gocql.UUID

		// Loop to fetch pages until the limit is reached or there's no more data
		for count := 0; count < limit; {
			// Define your paginated query
			var query string
			var iter *gocql.Iter

			if lastID == (gocql.UUID{}) {
				// First page - no WHERE clause
				query = `SELECT id, event_type, event_data, retry_count, last_retry_timestamp, created_at 
						 FROM failed_cloudevents LIMIT ?`
				iter = c.session.Query(query, limit-count).WithContext(ctx).Iter()
			} else {
				// Subsequent pages - use WHERE for pagination
				query = `SELECT id, event_type, event_data, retry_count, last_retry_timestamp, created_at 
						 FROM failed_cloudevents WHERE id > ? LIMIT ?`
				iter = c.session.Query(query, lastID, limit-count).WithContext(ctx).Iter()
			}

			var event data_access.FailedCloudEvent
			var id gocql.UUID
			fetchedCount := 0

			// Loop through the results of the current page
			for iter.Scan(&id, &event.EventType, &event.EventData, &event.RetryCount,
				&event.LastRetryTimestamp, &event.CreatedAt) {

				event.ID = uuid.UUID(id)

				// Yield the event to the consumer.
				// If yield returns false, the consumer has stopped the loop, so we should stop too.
				if !yield(event) {
					// Consumer broke the loop. Close the iterator and return.
					iter.Close()
					return
				}

				lastID = id
				count++
				fetchedCount++
			}

			// Check for any errors during iteration
			if err := iter.Close(); err != nil {
				logger.ErrorContext(ctx, "failed to stream failed CloudEvents", zap.Error(err))
				return // Return here to stop the iterator
			}

			// If we didn't get any results on the last page, we're done.
			if fetchedCount == 0 {
				break
			}
		}
	}
}

// UpdateFailedCloudEventRetry updates retry information for a failed CloudEvent
func (c *CassandraHandler) UpdateFailedCloudEventRetry(ctx context.Context, id uuid.UUID, retryCount int) error {
	// Tracing handled by external library
	logger := logging.GetLogger(ctx)

	query := `UPDATE failed_cloudevents SET retry_count = ?, last_retry_timestamp = ? WHERE id = ?`

	err := c.executeWithSessionRecreation(ctx, func() error {
		return c.session.Query(query, retryCount, time.Now(), gocql.UUID(id)).WithContext(ctx).Exec()
	}, "UpdateFailedCloudEventRetry")

	if err != nil {
		logger.ErrorContext(ctx, "failed to update failed CloudEvent retry",
			zap.String("id", id.String()), zap.Error(err))
		return err
	}

	logger.DebugContext(ctx, "updated failed CloudEvent retry",
		zap.String("id", id.String()), zap.Int("retryCount", retryCount))
	return nil
}

// DeleteFailedCloudEvent removes a successfully sent CloudEvent from the failed events table
func (c *CassandraHandler) DeleteFailedCloudEvent(ctx context.Context, id uuid.UUID) error {
	// Tracing handled by external library
	logger := logging.GetLogger(ctx)

	query := `DELETE FROM failed_cloudevents WHERE id = ?`

	err := c.executeWithSessionRecreation(ctx, func() error {
		return c.session.Query(query, gocql.UUID(id)).WithContext(ctx).Exec()
	}, "DeleteFailedCloudEvent")

	if err != nil {
		logger.ErrorContext(ctx, "failed to delete failed CloudEvent",
			zap.String("id", id.String()), zap.Error(err))
		return err
	}

	logger.InfoContext(ctx, "deleted successfully sent CloudEvent", zap.String("id", id.String()))
	return nil
}

// Ensure CassandraHandler implements CloudEventsResilienceHandler
var _ data_access.CloudEventsResilienceHandler = (*CassandraHandler)(nil)

// --- Simple Leader Election for Retry Coordination ---

// TryBecomeLeader attempts to become or renew leadership for retry processing
func (c *CassandraHandler) TryBecomeLeader(ctx context.Context, instanceID string, leaseDuration time.Duration) (bool, error) {
	// Tracing handled by external library
	logger := logging.GetLogger(ctx)

	serviceName := "cloudevents_retry"
	newExpiry := time.Now().Add(leaseDuration)

	// Try to become leader using LWT (lightweight transaction)
	// First, try to update if we are already the leader (renewal)
	query := `UPDATE retry_leader 
			  SET lease_expires = ?
			  WHERE service_name = ?
			  IF instance_id = ?`

	var applied bool
	err := c.executeWithSessionRecreation(ctx, func() error {
		// For LWT queries, use ScanCAS which properly handles the [applied] column
		// When the UPDATE fails, it returns only the column(s) checked in IF clause
		var currentInstanceID string

		casApplied, err := c.session.Query(query, newExpiry, serviceName, instanceID).
			WithContext(ctx).ScanCAS(&currentInstanceID)
		applied = casApplied
		return err
	}, "TryBecomeLeader-Renewal")

	// If we're not the current leader, try to take over if lease expired
	if err == nil && !applied {
		query = `UPDATE retry_leader 
				 SET instance_id = ?, lease_expires = ?
				 WHERE service_name = ?
				 IF lease_expires < ?`

		err = c.executeWithSessionRecreation(ctx, func() error {
			// When the UPDATE fails, it returns only the column(s) checked in IF clause
			var currentLeaseExpires time.Time

			casApplied, err := c.session.Query(query, instanceID, newExpiry, serviceName, time.Now()).
				WithContext(ctx).ScanCAS(&currentLeaseExpires)
			applied = casApplied
			return err
		}, "TryBecomeLeader-Takeover")

		// If no row exists, try to insert
		if err == nil && !applied {
			query = `INSERT INTO retry_leader (service_name, instance_id, lease_expires) 
					 VALUES (?, ?, ?)
					 IF NOT EXISTS`

			err = c.executeWithSessionRecreation(ctx, func() error {
				// INSERT IF NOT EXISTS returns all columns when it fails (row exists)
				// We need variables for all columns even if we don't use them
				var existingServiceName string
				var existingInstanceID string
				var existingLeaseExpires time.Time

				casApplied, err := c.session.Query(query, serviceName, instanceID, newExpiry).
					WithContext(ctx).ScanCAS(&existingServiceName, &existingInstanceID, &existingLeaseExpires)
				applied = casApplied
				return err
			}, "TryBecomeLeader-Insert")
		}
	}

	if err != nil {
		logger.ErrorContext(ctx, "failed to acquire/renew leadership", zap.Error(err))
		return false, err
	}

	if applied {
		logger.DebugContext(ctx, "acquired/renewed retry leadership",
			zap.String("instance", instanceID), zap.Time("expires", newExpiry))
	}

	return applied, nil
}

// UpsertEventV3 inserts or updates an event in the events_v3 table
// For v3 endpoint specifically - overwrites duplicates based on (namespace, context, event_name)
func (c *CassandraHandler) UpsertEventV3(traceCtx context.Context, namespace, eventContext, eventName, source string, details json.RawMessage, timestamp time.Time) error {
	logger := logging.GetLogger(traceCtx)

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		// Use LWT (Lightweight Transaction) to atomically insert if not exists
		insertQuery := `INSERT INTO events_v3 (namespace, context, event_name, source, details, timestamp, created_at, updated_at) 
						VALUES (?, ?, ?, ?, ?, ?, ?, ?) IF NOT EXISTS`

		var applied bool
		// Variables to scan existing row if IF NOT EXISTS fails
		// When CAS fails, Cassandra returns all columns in this order:
		// PK: namespace, context
		// Clustering: event_name
		// Other (alphabetical): created_at, details, source, timestamp, updated_at
		var existingNamespace, existingContext, existingEventName, existingSource string
		var existingDetails []byte
		var existingCreatedAt, existingTimestamp, existingUpdatedAt time.Time

		// Try to insert as new record
		casApplied, err := c.session.Query(insertQuery,
			namespace,
			eventContext,
			eventName,
			source,
			details,
			timestamp,
			timestamp, // created_at
			timestamp, // updated_at
		).WithContext(traceCtx).ScanCAS(
			&existingNamespace,
			&existingContext,
			&existingEventName,
			&existingCreatedAt,
			&existingDetails,
			&existingSource,
			&existingTimestamp,
			&existingUpdatedAt,
		)
		applied = casApplied

		if err != nil {
			logger.ErrorContext(traceCtx, "Failed to insert event into events_v3 table",
				zap.Error(err),
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.String("event_name", eventName),
				zap.String("source", source))
			return err
		}

		if applied {
			// Successfully inserted new record
			logger.InfoContext(traceCtx, "Inserted new event into events_v3",
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.String("event_name", eventName),
				zap.String("source", source))
		} else {
			// Record exists - update it preserving created_at
			updateQuery := `INSERT INTO events_v3 (namespace, context, event_name, source, details, timestamp, created_at, updated_at) 
							VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

			if err := c.session.Query(updateQuery,
				namespace,
				eventContext,
				eventName,
				source,
				details,
				timestamp,         // event timestamp
				existingCreatedAt, // preserve original created_at
				time.Now(),        // updated_at = current time
			).WithContext(traceCtx).Exec(); err != nil {
				logger.ErrorContext(traceCtx, "Failed to update event in events_v3 table",
					zap.Error(err),
					zap.String("namespace", namespace),
					zap.String("context", eventContext),
					zap.String("event_name", eventName),
					zap.String("source", source))
				return err
			}

			logger.DebugContext(traceCtx, "Updated existing event in events_v3",
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.String("event_name", eventName),
				zap.String("source", source))
		}

		return nil
	}, "upsertEventV3")

	return err
}

// BulkUpsertEventsV3 inserts or updates multiple events in the events_v3 table.
// Events are grouped by partition key (namespace, context). Each group is written
// with one SELECT + one unlogged BATCH, reducing Cassandra round-trips from 2N
// (per-event LWT) to 2 per distinct partition.
// Partition groups are processed in parallel, capped at 20 concurrent goroutines.
func (c *CassandraHandler) BulkUpsertEventsV3(traceCtx context.Context, events []data_access.EventV3UpsertRecord) error {
	if len(events) == 0 {
		return nil
	}

	type partitionKey struct{ namespace, context string }
	groups := make(map[partitionKey][]data_access.EventV3UpsertRecord)
	for _, ev := range events {
		key := partitionKey{ev.Namespace, ev.Context}
		groups[key] = append(groups[key], ev)
	}

	const maxConcurrency = 20
	sem := semaphore.NewWeighted(maxConcurrency)
	g, gctx := errgroup.WithContext(traceCtx)

	for pk, partEvents := range groups {
		pk, partEvents := pk, partEvents
		g.Go(func() error {
			if err := sem.Acquire(gctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)
			return c.upsertPartitionEventsV3(gctx, pk.namespace, pk.context, partEvents)
		})
	}

	return g.Wait()
}

// upsertPartitionEventsV3 writes events that share the same (namespace, context) partition.
// It fetches existing created_at values keyed by event_name first (to preserve them on update),
// then writes all rows in a single unlogged batch.
func (c *CassandraHandler) upsertPartitionEventsV3(traceCtx context.Context, namespace, eventContext string, events []data_access.EventV3UpsertRecord) error {
	logger := logging.GetLogger(traceCtx)

	return c.executeWithSessionRecreation(traceCtx, func() error {
		selectQuery := `SELECT event_name, created_at FROM events_v3 WHERE namespace = ? AND context = ?`
		iter := c.session.Query(selectQuery, namespace, eventContext).WithContext(traceCtx).Iter()
		existingCreatedAt := make(map[string]time.Time)
		var scannedEventName string
		var scannedCreatedAt time.Time
		for iter.Scan(&scannedEventName, &scannedCreatedAt) {
			existingCreatedAt[scannedEventName] = scannedCreatedAt
		}
		if err := iter.Close(); err != nil {
			logger.ErrorContext(traceCtx, "Failed to fetch existing events for partition",
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.Error(err))
			return fmt.Errorf("failed to fetch existing events for partition (%s, %s): %w", namespace, eventContext, err)
		}

		insertQuery := `INSERT INTO events_v3 (namespace, context, event_name, source, details, timestamp, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
		batch := c.session.NewBatch(gocql.UnloggedBatch).WithContext(traceCtx)
		now := time.Now()
		for _, ev := range events {
			createdAt := ev.Timestamp
			if ca, exists := existingCreatedAt[ev.EventName]; exists {
				createdAt = ca
			}
			batch.Query(insertQuery, ev.Namespace, ev.Context, ev.EventName, ev.Source, ev.Details, ev.Timestamp, createdAt, now)
		}

		if err := c.session.ExecuteBatch(batch); err != nil {
			logger.ErrorContext(traceCtx, "Failed to batch insert events",
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.Error(err))
			return fmt.Errorf("failed to batch insert events for partition (%s, %s): %w", namespace, eventContext, err)
		}

		logger.InfoContext(traceCtx, "Batch inserted events",
			zap.String("namespace", namespace),
			zap.String("context", eventContext),
			zap.Int("count", len(events)))
		return nil
	}, "upsertPartitionEventsV3")
}

// BulkUpsertStatsV3 inserts or updates stats in the stats_v3 table for multiple events.
// stats_v3 stores one row per (namespace, context) with the latest event_name and timestamp.
// Events are deduplicated to the latest per (namespace, context), then grouped by namespace
// partition and written with one SELECT + one unlogged BATCH per partition.
func (c *CassandraHandler) BulkUpsertStatsV3(traceCtx context.Context, events []data_access.EventV3UpsertRecord) error {
	if len(events) == 0 {
		return nil
	}

	// Dedup to latest event per (namespace, context) — stats_v3 holds only one row per pair.
	type statsKey struct{ namespace, context string }
	latest := make(map[statsKey]data_access.EventV3UpsertRecord)
	for _, ev := range events {
		key := statsKey{ev.Namespace, ev.Context}
		if existing, ok := latest[key]; !ok || ev.Timestamp.After(existing.Timestamp) {
			latest[key] = ev
		}
	}

	// Group deduplicated events by namespace (partition key).
	groups := make(map[string][]data_access.EventV3UpsertRecord)
	for _, ev := range latest {
		groups[ev.Namespace] = append(groups[ev.Namespace], ev)
	}

	const maxConcurrency = 20
	sem := semaphore.NewWeighted(maxConcurrency)
	g, gctx := errgroup.WithContext(traceCtx)

	for ns, partEvents := range groups {
		ns, partEvents := ns, partEvents
		g.Go(func() error {
			if err := sem.Acquire(gctx, 1); err != nil {
				return err
			}
			defer sem.Release(1)
			return c.upsertPartitionStatsV3(gctx, ns, partEvents)
		})
	}

	return g.Wait()
}

// upsertPartitionStatsV3 writes stats rows that share the same namespace partition.
func (c *CassandraHandler) upsertPartitionStatsV3(traceCtx context.Context, namespace string, events []data_access.EventV3UpsertRecord) error {
	logger := logging.GetLogger(traceCtx)

	return c.executeWithSessionRecreation(traceCtx, func() error {
		selectQuery := `SELECT context, created_at FROM stats_v3 WHERE namespace = ?`
		iter := c.session.Query(selectQuery, namespace).WithContext(traceCtx).Iter()
		existingCreatedAt := make(map[string]time.Time)
		var scannedContext string
		var scannedCreatedAt time.Time
		for iter.Scan(&scannedContext, &scannedCreatedAt) {
			existingCreatedAt[scannedContext] = scannedCreatedAt
		}
		if err := iter.Close(); err != nil {
			logger.ErrorContext(traceCtx, "Failed to fetch existing stats for namespace",
				zap.String("namespace", namespace),
				zap.Error(err))
			return fmt.Errorf("failed to fetch existing stats for namespace %s: %w", namespace, err)
		}

		insertQuery := `INSERT INTO stats_v3 (namespace, context, event_name, timestamp, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`
		batch := c.session.NewBatch(gocql.UnloggedBatch).WithContext(traceCtx)
		now := time.Now()
		for _, ev := range events {
			createdAt := ev.Timestamp
			if ca, exists := existingCreatedAt[ev.Context]; exists {
				createdAt = ca
			}
			batch.Query(insertQuery, ev.Namespace, ev.Context, ev.EventName, ev.Timestamp, createdAt, now)
		}

		if err := c.session.ExecuteBatch(batch); err != nil {
			logger.ErrorContext(traceCtx, "Failed to batch insert stats",
				zap.String("namespace", namespace),
				zap.Error(err))
			return fmt.Errorf("failed to batch insert stats for namespace %s: %w", namespace, err)
		}

		logger.InfoContext(traceCtx, "Batch inserted stats",
			zap.String("namespace", namespace),
			zap.Int("count", len(events)))
		return nil
	}, "upsertPartitionStatsV3")
}

// UpsertStatsV3 inserts or updates latest event stats in the stats_v3 table
// Replaces the previous event for this (namespace, context) regardless of event_name
func (c *CassandraHandler) UpsertStatsV3(traceCtx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return c.upsertStatsRow(traceCtx, statsV3Table, namespace, eventContext, eventName, timestamp)
}

// UpsertFilteredStatsV3 inserts or updates latest event stats in the filtered stats table.
// Callers must apply the filtered event-name allowlist before invoking this method.
func (c *CassandraHandler) UpsertFilteredStatsV3(traceCtx context.Context, namespace, eventContext, eventName string, timestamp time.Time) error {
	return c.upsertStatsRow(traceCtx, filteredStatsV3Table, namespace, eventContext, eventName, timestamp)
}

// upsertStatsRow is the shared LWT upsert for stats_v3-shaped tables.
// table must be a trusted, code-controlled identifier (not user input).
func (c *CassandraHandler) upsertStatsRow(traceCtx context.Context, table, namespace, eventContext, eventName string, timestamp time.Time) error {
	logger := logging.GetLogger(traceCtx)

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		// Use LWT to atomically insert if not exists
		insertQuery := fmt.Sprintf(`INSERT INTO %s (namespace, context, event_name, timestamp, created_at, updated_at)
						VALUES (?, ?, ?, ?, ?, ?) IF NOT EXISTS`, table)

		// Variables to scan existing row if IF NOT EXISTS fails.
		// When CAS fails, Cassandra returns all columns in this order:
		// PK: namespace, context
		// Other (alphabetical): created_at, event_name, timestamp, updated_at
		var existingNamespace, existingContext, existingEventName string
		var existingCreatedAt, existingTimestamp, existingUpdatedAt time.Time

		applied, err := c.session.Query(insertQuery,
			namespace,
			eventContext,
			eventName,
			timestamp,
			timestamp, // created_at
			timestamp, // updated_at
		).WithContext(traceCtx).ScanCAS(
			&existingNamespace,
			&existingContext,
			&existingCreatedAt,
			&existingEventName,
			&existingTimestamp,
			&existingUpdatedAt,
		)

		if err != nil {
			logger.ErrorContext(traceCtx, "Failed to insert stats",
				zap.Error(err),
				zap.String("table", table),
				zap.String("namespace", namespace),
				zap.String("context", eventContext),
				zap.String("event_name", eventName))
			return err
		}

		if !applied {
			// Record exists - update it preserving created_at, replacing event_name
			updateQuery := fmt.Sprintf(`INSERT INTO %s (namespace, context, event_name, timestamp, created_at, updated_at)
							VALUES (?, ?, ?, ?, ?, ?)`, table)

			if err := c.session.Query(updateQuery,
				namespace,
				eventContext,
				eventName,
				timestamp,         // event timestamp
				existingCreatedAt, // preserve original created_at
				time.Now(),        // updated_at = current time
			).WithContext(traceCtx).Exec(); err != nil {
				logger.ErrorContext(traceCtx, "Failed to update stats",
					zap.Error(err),
					zap.String("table", table),
					zap.String("namespace", namespace),
					zap.String("context", eventContext),
					zap.String("event_name", eventName))
				return err
			}
		}

		logger.DebugContext(traceCtx, "Upserted stats",
			zap.String("table", table),
			zap.String("namespace", namespace),
			zap.String("context", eventContext),
			zap.String("event_name", eventName))

		return nil
	}, "upsertStats:"+table)

	return err
}

// GetStatsV3 retrieves all stats records for a given namespace from the stats_v3 table
func (c *CassandraHandler) GetStatsV3(traceCtx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return c.getStatsRows(traceCtx, statsV3Table, namespace)
}

// GetFilteredStatsV3 retrieves all stats records for a namespace from the filtered stats table.
func (c *CassandraHandler) GetFilteredStatsV3(traceCtx context.Context, namespace string) ([]data_access.StatsV3Record, error) {
	return c.getStatsRows(traceCtx, filteredStatsV3Table, namespace)
}

// getStatsRows is the shared read for stats_v3-shaped tables.
// table must be a trusted, code-controlled identifier (not user input).
func (c *CassandraHandler) getStatsRows(traceCtx context.Context, table, namespace string) ([]data_access.StatsV3Record, error) {
	logger := logging.GetLogger(traceCtx)

	var records []data_access.StatsV3Record

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		records = nil
		query := fmt.Sprintf(`SELECT context, event_name, timestamp, created_at, updated_at
				  FROM %s
				  WHERE namespace = ?`, table)

		iter := c.session.Query(query, namespace).WithContext(traceCtx).Iter()

		var context, eventName string
		var timestamp, createdAt, updatedAt time.Time

		for iter.Scan(&context, &eventName, &timestamp, &createdAt, &updatedAt) {
			records = append(records, data_access.StatsV3Record{
				Context:   context,
				EventName: eventName,
				Timestamp: timestamp,
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			})
		}

		if err := iter.Close(); err != nil {
			logger.ErrorContext(traceCtx, "Failed to retrieve stats",
				zap.Error(err),
				zap.String("table", table),
				zap.String("namespace", namespace))
			return err
		}

		logger.DebugContext(traceCtx, "Retrieved stats",
			zap.String("table", table),
			zap.String("namespace", namespace),
			zap.Int("record_count", len(records)))

		return nil
	}, "getStats:"+table)

	if err != nil {
		return nil, err
	}

	return records, nil
}

// GetEventsV3 retrieves all events for a given namespace and context from the events_v3 table, ordered by timestamp descending
func (c *CassandraHandler) GetEventsV3(traceCtx context.Context, namespace, eventContext string) ([]data_access.EventV3Record, error) {
	logger := logging.GetLogger(traceCtx)

	var records []data_access.EventV3Record

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		records = nil
		// Note: Cannot ORDER BY timestamp as it's not part of the clustering key
		// Ordering by event_name (clustering key), will sort by timestamp in Go
		query := `SELECT event_name, source, details, timestamp, created_at, updated_at 
				  FROM events_v3 
				  WHERE namespace = ? AND context = ?`

		iter := c.session.Query(query, namespace, eventContext).WithContext(traceCtx).Iter()

		for {
			record := data_access.EventV3Record{}
			if !iter.Scan(&record.EventName, &record.Source, &record.Details, &record.Timestamp, &record.CreatedAt, &record.UpdatedAt) {
				break
			}

			records = append(records, record)
		}

		if err := iter.Close(); err != nil {
			logger.ErrorContext(traceCtx, "Failed to retrieve events from events_v3 table",
				zap.Error(err),
				zap.String("namespace", namespace),
				zap.String("context", eventContext))
			return err
		}

		logger.DebugContext(traceCtx, "Retrieved events from events_v3",
			zap.String("namespace", namespace),
			zap.String("context", eventContext),
			zap.Int("event_count", len(records)))

		return nil
	}, "getEventsV3")

	if err != nil {
		return nil, err
	}

	return records, nil
}
