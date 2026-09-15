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
	"log"

	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/common"
)

func SetupBootstrapLogger(serviceName string, serviceVersion string) (*otelzap.Logger, func()) {
	if serviceVersion == "" {
		serviceVersion = "bootstrap"
	}

	return SetupLoggingFromConfig(
		&LoggingConfig{
			Level:            "debug",
			ZapConfiguration: "development",
		},
		&common.TelemetryConfig{
			ServiceName:     serviceName,
			ServiceVersion:  serviceVersion,
			EnvironmentName: "bootstrap",
		},
	)
}

type ZapIoWriter struct {
	logger *zap.Logger
}

func NewLoggerWithZapWriter(zapLogger *zap.Logger) *log.Logger {
	return log.New(&ZapIoWriter{logger: zapLogger}, "", 0)
}

func (z *ZapIoWriter) Write(p []byte) (int, error) {
	z.logger.Error(string(p))
	return len(p), nil
}
