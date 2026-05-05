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
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/zapotelspan"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

func InitializeLogger(cfg *config.RevalConfig) (*zap.Logger, *zap.AtomicLevel, func()) {
	var zapConfig zap.Config

	level, err := zapcore.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zapcore.InfoLevel
	}

	switch cfg.Logging.ZapConfiguration {
	case "production":
		zapConfig = zap.NewProductionConfig()
	case "development":
		zapConfig = zap.NewDevelopmentConfig()
	}
	zapConfig.DisableStacktrace = true

	zapConfig.Level.SetLevel(level)

	zapOptions := []zap.Option{
		zapotelspan.WrapCoreWithZapOtelAdaptor(zapConfig.Level.Level()),
		zap.Fields(
			zap.String(string(semconv.ServiceNameKey), cfg.Telemetry.ServiceName),
			zap.String(string(semconv.ServiceVersionKey), cfg.Telemetry.ServiceVersion),
			zap.String(string(semconv.DeploymentEnvironmentNameKey), cfg.Telemetry.DeploymentEnvironmentName),
		),
		zap.WithCaller(true),
	}

	logger, err := zapConfig.Build(zapOptions...)
	if err != nil {
		zap.S().Warnf("failed to build logger: %v. Using example logger.", err)
		logger = zap.NewExample()
	}

	undoReplace := zap.ReplaceGlobals(logger)
	return logger, &zapConfig.Level, undoReplace

}

// SetupBootstrapLogger initializes the logger during the bootstrap process.
// It is used to initialize the logger before the configuration is loaded.
// Asumes some possible environment variables
// Defaults to production (full structured logging)
func SetupBootstrapLogger(compiledVersion string) (*zap.Logger, func()) {
	serviceVersion := os.Getenv("REVAL_TELEMETRY_SERVICE_VERSION")
	if serviceVersion == "" {
		serviceVersion = compiledVersion
	}

	deploymentEnvironmentName := os.Getenv("REVAL_TELEMETRY_DEPLOYMENT_ENVIRONMENT_NAME")
	if deploymentEnvironmentName == "" {
		deploymentEnvironmentName = "bootstrap"
	}

	config := &config.RevalConfig{
		Telemetry: config.TelemetryConfig{
			ServiceName:               config.ApiSvcName,
			ServiceVersion:            serviceVersion,
			DeploymentEnvironmentName: deploymentEnvironmentName,
		},
		Logging: config.LoggingConfig{
			// This is use till the configuration is ready,
			// better output messages compatible with production
			ZapConfiguration: "production",
		},
	}
	logger, _, undoReplace := InitializeLogger(config)
	return logger, undoReplace
}

func PanicHandler(logger *zap.Logger) func() {
	return func() {
		if err := recover(); err != nil {
			logger.Error("Panic handler!", zap.Any("panic", err))
		}
	}
}
