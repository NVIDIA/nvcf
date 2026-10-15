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
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/interfaces"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/registrations"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/codex"
)

type Connections struct {
	DbHandler   data_access.DBHandler   // For V1 operations
	DbHandlerV2 data_access.DBHandlerV2 // For V2 operations
}

func InitConns(cfg config.Config, logger *otelzap.Logger) (Connections, error) {
	db, err := registrations.NewDBConnection(cfg.Database)
	if err != nil {
		errMsg := fmt.Sprintf("could not connect to db: %s", err.Error())
		logger.Fatal(errMsg) // Keep fatal as it implies unrecoverable state
		// Note: Fatal will exit, so error return might not be reached, but keep for consistency
		return Connections{}, fmt.Errorf("%s", errMsg)
	}

	// Attempt to get the V2 handler via type assertion.
	// This assumes the underlying implementation (e.g., CassandraHandler)
	// implements both interfaces.
	dbV2, ok := db.(data_access.DBHandlerV2)
	if !ok {
		// If the assertion fails, log a warning but continue with V1 handler.
		// This allows the service to potentially still operate V1 endpoints.
		// Depending on requirements, this could be a fatal error instead.
		logger.Warn("database handler does not implement v2 interface. v2 endpoints will fail")
		// Return only the V1 handler populated.
		return Connections{DbHandler: db}, nil
	}

	// Return both V1 and V2 handlers populated.
	return Connections{DbHandler: db, DbHandlerV2: dbV2}, nil
}

type Server struct {
	conns                   Connections
	logger                  *otelzap.Logger
	codex                   *codex.Codex
	eventPub                interfaces.EventPublisher
	version                 string
	httpClientConfig        *config.HTTPClientConfig
	paginationConfig        config.PaginationConfig
	statsConfig             config.StatsConfig
	statsEventNames         map[string]struct{}
	filteredStatsEventNames map[string]struct{}
}

func NewServer(conns Connections, logger *otelzap.Logger, publisher interfaces.EventPublisher, version string, httpConfig *config.HTTPClientConfig, paginationConfig config.PaginationConfig, statsConfig config.StatsConfig) *Server {
	statsEventNames := make(map[string]struct{})
	for _, eventName := range statsConfig.StatsEnabledEventNames {
		statsEventNames[eventName] = struct{}{}
	}

	filteredStatsEventNames := make(map[string]struct{})
	for _, eventName := range statsConfig.FilteredStatsEnabledEventNames {
		filteredStatsEventNames[eventName] = struct{}{}
	}

	return &Server{
		conns,
		logger,
		codex.NewCodex(logger),
		publisher,
		version,
		httpConfig,
		paginationConfig.WithDefaults(),
		statsConfig,
		statsEventNames,
		filteredStatsEventNames,
	}
}

func (s *Server) WriteStageTransitionEvent(traceCtx context.Context, ste types.StageTransitionEvent) error {
	// Tracing handled by external library

	logger := logging.GetLogger(traceCtx)

	logger.InfoContext(traceCtx, "writing stage transition event (v1)", zap.String("event", ste.Event))
	if s.conns.DbHandler == nil {
		logger.ErrorContext(traceCtx, "v1 db handler not initialized")
		return fmt.Errorf("internal server error: V1 DB handler unavailable")
	}
	if err := s.conns.DbHandler.WriteStageTransitionEvent(traceCtx, ste); err != nil {
		if strings.Contains(err.Error(), "DUPLICATE EVENT") {
			logger.WarnContext(traceCtx, "duplicate event received (v1)", zap.String("event", ste.Event), zap.Error(err))
			// Note: Returning the duplicate error might be desired by callers.
			return err
		}
		logger.ErrorContext(traceCtx, "failed to write v1 message record to db", zap.Error(err))
		return err
	}

	return nil
}

// WriteDeploymentStageTransitionEvent uses the V2 DB handler.
func (s *Server) WriteDeploymentStageTransitionEvent(traceCtx context.Context, dste types.DeploymentStageTransitionEvent) error {
	// Tracing handled by external library

	logger := logging.GetLogger(traceCtx)

	logger.InfoContext(traceCtx, "writing deployment stage transition event (v2)", zap.String("event", dste.Event))
	if s.conns.DbHandlerV2 == nil {
		logger.ErrorContext(traceCtx, "v2 db handler not initialized")
		return fmt.Errorf("internal server error: V2 DB handler unavailable")
	}
	if err := s.conns.DbHandlerV2.WriteDeploymentStageTransitionEvent(traceCtx, dste); err != nil {
		if strings.Contains(err.Error(), "DUPLICATE EVENT") {
			logger.WarnContext(traceCtx, "duplicate deployment event received (v2)", zap.String("event", dste.Event), zap.Error(err))
			// Note: Returning the duplicate error might be desired by callers.
			return err
		}
		logger.ErrorContext(traceCtx, "failed to write v2 deployment message record to db", zap.Error(err))
		return err
	}

	return nil
}
