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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/gocql/gocql"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	sq "github.com/Masterminds/squirrel"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/configutil"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

// isConnectionError checks if the error is related to connection issues
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	connectionErrors := []string{
		"no hosts available in the pool",
		"connection refused",
		"no connection available",
		"connection reset by peer",
		"broken pipe",
		"network is unreachable",
		"host is down",
		"session has been closed",
	}

	for _, connErr := range connectionErrors {
		if strings.Contains(errStr, connErr) {
			return true
		}
	}

	return false
}

func (c *CassandraHandler) timeInsert(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	if dste.Event == "pending" {
		logger.DebugContext(traceCtx, "discarding pending event", zap.String("event", dste.Event))
		return nil
	}

	query, args, err := buildTimeInsert(dste)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to generate insert for deployment stage transition event time record", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to write message to cassandra", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted event time record", zap.String("event", dste.Event))
		return nil
	}, "timeInsert")
}

func buildTimeInsert(dste types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"timestamp_key", "nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		gocql.UUIDFromTime(dste.Timestamp),
		dste.NcaId,
		gocql.UUID(dste.FunctionId),
		gocql.UUID(dste.FunctionVersionId),
		dste.InstanceId,
		gocql.UUID(dste.DeploymentId),
		dste.Event,
		dste.EventType,
		dste.Timestamp.UnixMilli(),
		dste.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_timestamp").Columns(columns...).Values(values...)

	// Generate the CQL query
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}

	return query, args, nil
}

func (c *CassandraHandler) functionIdInsert(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildFunctionIdInsert(event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build function id insert", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to write message to cassandra", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully inserted event function id record", zap.String("event", event.Event))
		return nil
	}, "functionIdInsert")
}

func buildFunctionIdInsert(event types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		event.NcaId,
		gocql.UUID(event.FunctionId),
		gocql.UUID(event.FunctionVersionId),
		event.InstanceId,
		gocql.UUID(event.DeploymentId),
		event.Event,
		event.EventType,
		event.Timestamp.UnixMilli(),
		event.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_function_id").Columns(columns...).Values(values...)
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// IndexByFunctionVersionId inserts a record into lookup_by_function_version_id table
func (c *CassandraHandler) functionVersionIdInsert(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildFunctionVersionIdInsert(event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build index by function version id insert", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to index by function version id", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully indexed by function version id", zap.String("functionVersionId", event.FunctionVersionId.String()))
		return nil
	}, "functionVersionIdInsert")
}

func buildFunctionVersionIdInsert(event types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		event.NcaId,
		gocql.UUID(event.FunctionId),
		gocql.UUID(event.FunctionVersionId),
		event.InstanceId,
		gocql.UUID(event.DeploymentId),
		event.Event,
		event.EventType,
		event.Timestamp.UnixMilli(),
		event.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_function_version_id").Columns(columns...).Values(values...)
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// IndexByInstanceId inserts a record into lookup_by_instance_id table
func (c *CassandraHandler) instanceIdInsert(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildInstanceIdInsert(event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build index by instance id insert", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to index by instance id", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully indexed by instance id", zap.String("instanceId", event.InstanceId))
		return nil
	}, "instanceIdInsert")
}

func buildInstanceIdInsert(event types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		event.NcaId,
		gocql.UUID(event.FunctionId),
		gocql.UUID(event.FunctionVersionId),
		event.InstanceId,
		gocql.UUID(event.DeploymentId),
		event.Event,
		event.EventType,
		event.Timestamp.UnixMilli(),
		event.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_instance_id").Columns(columns...).Values(values...)
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// IndexByDeploymentId inserts a record into lookup_by_deployment_id table
func (c *CassandraHandler) deploymentIdInsert(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildDeploymentIdInsert(event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build index by deployment id insert", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to index by deployment id", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully indexed by deployment id")
		return nil
	}, "deploymentIdInsert")
}

func buildDeploymentIdInsert(event types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		event.NcaId,
		gocql.UUID(event.FunctionId),
		gocql.UUID(event.FunctionVersionId),
		event.InstanceId,
		gocql.UUID(event.DeploymentId),
		event.Event,
		event.EventType,
		event.Timestamp.UnixMilli(),
		event.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_deployment_id").Columns(columns...).Values(values...)
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// IndexByNcaId inserts a record into lookup_by_nca_id table
func (c *CassandraHandler) ncaIdInsert(traceCtx context.Context, event types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library
	logger := logging.GetLogger(traceCtx)

	query, args, err := buildNcaIdInsert(event)
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to build index by nca id insert", zap.Error(err))
		return err
	}
	logger.DebugContext(traceCtx, "query", zap.String("query_statement", query))

	return c.executeWithSessionRecreation(traceCtx, func() error {
		if err := c.session.Query(query, args...).WithContext(traceCtx).Exec(); err != nil {
			logger.ErrorContext(traceCtx, "failed to index by nca id", zap.Error(err))
			return err
		}
		logger.DebugContext(traceCtx, "successfully indexed by nca id", zap.String("ncaId", event.NcaId))
		return nil
	}, "ncaIdInsert")
}

func buildNcaIdInsert(event types.DeploymentStageTransitionEvent) (string, []interface{}, error) {
	columns := []string{"nca_id", "function_id", "function_version_id", "instance_id", "deployment_id", "event", "event_type", "timestamp", "details", "created_at"}
	values := []interface{}{
		event.NcaId,
		gocql.UUID(event.FunctionId),
		gocql.UUID(event.FunctionVersionId),
		event.InstanceId,
		gocql.UUID(event.DeploymentId),
		event.Event,
		event.EventType,
		event.Timestamp.UnixMilli(),
		event.Details,
		time.Now().UnixMilli(),
	}
	queryBuilder := sq.Insert("lookup_by_nca_id").Columns(columns...).Values(values...)
	query, args, err := queryBuilder.ToSql()
	if err != nil {
		return "", nil, err
	}
	return query, args, nil
}

// recreateSession recreates the entire gocql session when the connection pool is completely dead
func (c *CassandraHandler) recreateSession(traceCtx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	logger := logging.GetLogger(traceCtx)
	logger.WarnContext(traceCtx, "attempting to recreate cassandra session due to dead connection pool")

	// Close the old session
	if c.session != nil {
		c.session.Close()
	}

	// Create a new session with the same cluster config
	newSession, err := c.cluster.CreateSession()
	if err != nil {
		logger.ErrorContext(traceCtx, "failed to recreate cassandra session", zap.Error(err))
		return fmt.Errorf("failed to recreate session: %v", err)
	}

	// Replace the session
	c.session = newSession
	logger.WarnContext(traceCtx, "successfully recreated cassandra session")

	// Test the new session
	testErr := c.session.Query("DESCRIBE function_version_instances").WithContext(traceCtx).Exec()
	if testErr != nil {
		logger.WarnContext(traceCtx, "new session test query failed", zap.Error(testErr))
		return testErr
	}

	logger.WarnContext(traceCtx, "new cassandra session is healthy")
	return nil
}

// executeWithSessionRecreation provides the ultimate retry logic with session recreation
func (c *CassandraHandler) executeWithSessionRecreation(traceCtx context.Context, operation func() error, operationName string) error {
	baseDelay := 2 * time.Second
	logger := logging.GetLogger(traceCtx)
	sessionRecreated := false

	for attempt := 0; true; attempt++ {
		// Use non-blocking read lock for normal operations
		c.mutex.RLock()
		err := operation()
		c.mutex.RUnlock()

		if err == nil {
			if attempt > 0 {
				logger.InfoContext(traceCtx, "operation succeeded after session recovery",
					zap.String("operation", operationName),
					zap.Int("attempts", attempt+1),
					zap.Bool("session_recreated", sessionRecreated))
			}
			return nil
		}

		// Check if it's a connection-related error
		if isConnectionError(err) {
			logger.WarnContext(traceCtx, "connection error, considering session recreation",
				zap.String("operation", operationName),
				zap.Error(err),
				zap.Int("attempt", attempt+1))

			// try to recreate the session
			if !sessionRecreated {
				logger.WarnContext(traceCtx, "attempting session recreation for dead connection pool")
				recreateErr := c.recreateSession(traceCtx)
				if recreateErr == nil {
					sessionRecreated = true
					logger.InfoContext(traceCtx, "session recreation successful, retrying operation")
					// Don't sleep on the first retry after successful recreation
					continue
				} else {
					logger.ErrorContext(traceCtx, "session recreation failed", zap.Error(recreateErr))
				}
			}

			logger.DebugContext(traceCtx, "waiting before retry after session recreation attempt",
				zap.Duration("delay", baseDelay),
				zap.String("operation", operationName))
			time.Sleep(baseDelay)
			continue
		} else {
			return err
		}
	}
	return nil
}

// Interface assertions
var _ data_access.DBHandler = (*CassandraHandler)(nil)
var _ data_access.DBHandlerV2 = (*CassandraHandler)(nil)

type CassandraHandler struct {
	session *gocql.Session
	logger  *otelzap.Logger
	cluster *gocql.ClusterConfig // Store cluster config for session recreation
	mutex   sync.RWMutex         // Protect session recreation
}

// OTelQueryObserver Custom Query Observer for OpenTelemetry
type OTelQueryObserver struct{}

func (o *OTelQueryObserver) ObserveQuery(traceCtx context.Context, info gocql.ObservedQuery) {
	// This is a custom span creation to alter the start time.
	// Tracing level attributes now handled by external library
	_, currentFn := common.CurrentFunction(3)
	_, span := otel.Tracer(constants.SvcName).Start(
		traceCtx,
		currentFn,
		trace.WithTimestamp(info.Start),
	)
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("query.statement", info.Statement),
			attribute.String("query.keyspace", info.Keyspace),
			attribute.String("query.host", info.Host.String()),
			attribute.Int64("query.rows_returned", int64(info.Rows)),
			attribute.String("query.begin", info.Start.String()),
			attribute.String("query.end", info.End.String()),
			// attribute.String("error", info.Err.Error()),
			attribute.Int("query.attempts", info.Attempt),
			attribute.Int64("query.total_latency_in_ms", info.Metrics.TotalLatency/1_000_000),
		)
	}
}

func NewOtelQueryObserver() *OTelQueryObserver {
	return &OTelQueryObserver{}
}

type CassandraProvider struct {
	logger *otelzap.Logger
}

func NewCassandraProvider(logger *otelzap.Logger) *CassandraProvider {
	return &CassandraProvider{logger: logger}
}

func validateCassandraTLSConfig(cassandraConfig config.CassandraConfig) error {
	pathConfigured := cassandraConfig.PubKeyPath != "" ||
		cassandraConfig.PrivKeyPath != "" ||
		cassandraConfig.CACertPath != ""
	base64Configured := cassandraConfig.PubKeyB64 != "" ||
		cassandraConfig.PrivKeyB64 != "" ||
		cassandraConfig.CACertB64 != ""

	if pathConfigured && base64Configured {
		return fmt.Errorf("cassandra TLS configuration cannot mix file paths and base64 values")
	}
	if pathConfigured && (cassandraConfig.PubKeyPath == "" || cassandraConfig.PrivKeyPath == "") {
		return fmt.Errorf("cassandra TLS file configuration requires both pub-key-path and priv-key-path")
	}
	if base64Configured && (cassandraConfig.PubKeyB64 == "" || cassandraConfig.PrivKeyB64 == "") {
		return fmt.Errorf("cassandra TLS base64 configuration requires both pub-key-b64 and priv-key-b64")
	}

	return nil
}

func (p *CassandraProvider) NewConnection(config config.DBConfig) (data_access.DBHandler, error) {
	if err := validateCassandraTLSConfig(config.CassandraConfig); err != nil {
		return nil, err
	}

	cluster := gocql.NewCluster()
	cluster.Hosts = config.CassandraConfig.Hosts
	cluster.Port = config.CassandraConfig.Port
	cluster.Keyspace = config.CassandraConfig.Keyspace
	cluster.NumConns = config.CassandraConfig.NumConns
	cluster.Logger = logging.NewLoggerWithZapWriter(p.logger.Logger)

	queryObserver := NewOtelQueryObserver()
	// Attach OpenTelemetry Observer
	cluster.QueryObserver = queryObserver

	switch config.CassandraConfig.Consistency {
	case "QUORUM":
		cluster.Consistency = gocql.Quorum
	case "LOCAL_QUORUM":
		cluster.Consistency = gocql.LocalQuorum
	default:
		cluster.Consistency = gocql.LocalQuorum
	}

	if config.CassandraConfig.Username != "" && config.CassandraConfig.Password != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: config.CassandraConfig.Username,
			Password: config.CassandraConfig.Password,
		}
	}
	if config.CassandraConfig.PubKeyPath != "" && config.CassandraConfig.PrivKeyPath != "" {
		cluster.SslOpts = &gocql.SslOptions{
			CertPath:               config.CassandraConfig.PubKeyPath,
			KeyPath:                config.CassandraConfig.PrivKeyPath,
			CaPath:                 config.CassandraConfig.CACertPath,
			EnableHostVerification: !config.CassandraConfig.InsecureSkipVerify,
		}
	} else if config.CassandraConfig.PubKeyB64 != "" && config.CassandraConfig.PrivKeyB64 != "" {
		sslOpts, err := configutil.GetTLSConfigFromBase64(
			config.CassandraConfig.PubKeyB64,
			config.CassandraConfig.PrivKeyB64,
			config.CassandraConfig.CACertB64,
			config.CassandraConfig.InsecureSkipVerify,
		)
		if err != nil {
			return nil, fmt.Errorf("create Cassandra TLS config: %w", err)
		}
		cluster.SslOpts = &gocql.SslOptions{
			Config:                 sslOpts,
			EnableHostVerification: !config.CassandraConfig.InsecureSkipVerify,
		}
	}

	session, err := cluster.CreateSession()
	if err != nil {
		errMsg := fmt.Sprintf("could not connect to cassandra: %s", err.Error())
		p.logger.Fatal(errMsg)
	}
	p.logger.Warn("connected to cassandra",
		zap.Strings("hosts", cluster.Hosts),
		zap.Int("port", cluster.Port),
		zap.String("keyspace", cluster.Keyspace),
		zap.String("consistency", cluster.Consistency.String()),
		zap.Int("numConns", cluster.NumConns),
		zap.Duration("reconnectInterval", cluster.ReconnectInterval),
		zap.Duration("socketKeepalive", cluster.SocketKeepalive),
	)
	handler := &CassandraHandler{session: session, logger: p.logger, cluster: cluster}
	// Note: NewConnection currently returns DBHandler (V1). The caller will need
	// to assert the type to DBHandlerV2 if V2 functionality is needed.
	// Consider returning a struct containing both or changing the return type
	// if V2 becomes the primary interface.
	return handler, nil
}

var checkQueries = map[string]string{
	"system":          "SELECT * FROM system_schema.tables LIMIT 1",
	"table_events_v3": "SELECT * FROM events_v3 LIMIT 1",
}

type checkStatusResult struct {
	check string
	err   error
}

// runCheck runs a check on the Cassandra database and returns the result
// Designed to be run in a goroutine
func (c *CassandraHandler) runCheck(
	ctx context.Context,
	resultsQueue chan<- checkStatusResult,
	check string,
	query string,
) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Avoids crashing the entire application if a panic occurs
	defer func() {
		if r := recover(); r != nil {
			c.logger.ErrorContext(checkCtx,
				"panic during check",
				zap.String("check", check),
				zap.Any("error", r))
			resultsQueue <- checkStatusResult{
				check: check,
				err:   fmt.Errorf("panic during check '%s': %v", check, r),
			}
		}
	}()

	err := c.executeWithSessionRecreation(checkCtx, func() error {
		return c.session.Query(query).WithContext(checkCtx).Exec()
	}, fmt.Sprintf("runCheck_%s", check))

	resultsQueue <- checkStatusResult{
		check: check,
		err:   err,
	}
}

// CheckStatus checks the availability of the Cassandra database and tables
func (c *CassandraHandler) CheckStatus(ctx context.Context) map[string]error {
	// Tracing handled by external library
	traceCtx := ctx

	totalChecks := len(checkQueries)
	results := make(map[string]error, totalChecks)
	resultsQueue := make(chan checkStatusResult, totalChecks)

	for check, query := range checkQueries {
		go c.runCheck(traceCtx, resultsQueue, check, query)
	}

	for range totalChecks {
		result := <-resultsQueue
		results[result.check] = result.err
	}

	return results
}
