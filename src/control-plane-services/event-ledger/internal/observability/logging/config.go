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

package logging

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
)

type LoggingConfig struct {
	Level            string `mapstructure:"level"`
	ZapConfiguration string `mapstructure:"zap-configuration"`
}

func SetupLoggingFromConfig(loggingConfig *LoggingConfig, telemetryConfig *common.TelemetryConfig) (*otelzap.Logger, func()) {
	level, err := zapcore.ParseLevel(loggingConfig.Level)
	if err != nil {
		zap.S().Fatalf("failed to parse log level: %s", err.Error())
	}

	var zapConfig zap.Config
	if loggingConfig.ZapConfiguration == "production" {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
	}
	zapConfig.Level.SetLevel(level)
	zapConfig.DisableStacktrace = true

	zapLogger, err := zapConfig.Build(
		zapotelspan.WrapCoreWithZapOtelAdaptor(level),
		zap.Fields(
			zap.String(string(semconv.ServiceNameKey), telemetryConfig.ServiceName),
			zap.String(string(semconv.ServiceVersionKey), telemetryConfig.ServiceVersion),
			zap.String(string(semconv.DeploymentEnvironmentNameKey), telemetryConfig.EnvironmentName),
		),
		zap.WithCaller(false),
	)
	if err != nil {
		zap.S().Fatalf("failed to instantiate logger: %s", err.Error())
	}

	logger := otelzap.New(zapLogger, otelzap.WithMinLevel(level))
	undoZapGlobals := zap.ReplaceGlobals(zapLogger)
	undoOtelZapGlobals := otelzap.ReplaceGlobals(logger)

	return logger, func() {
		err := logger.Sync()
		if err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTTY) {
			fmt.Printf("error with logger sync: %v\n", err)
		}
		undoOtelZapGlobals()
		undoZapGlobals()
	}
}
