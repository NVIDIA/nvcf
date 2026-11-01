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

package tracing

import (
	"context"

	"go.uber.org/zap"
)

// NoOpProvider implements the TracingProvider interface with no-op behavior
type NoOpProvider struct {
	logger *zap.Logger
}

// NewNoOpProvider creates a new no-op tracing provider
func NewNoOpProvider(logger *zap.Logger) *NoOpProvider {
	return &NoOpProvider{
		logger: logger,
	}
}

// InitTracing initializes the no-op provider (does nothing)
func (n *NoOpProvider) InitTracing(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	n.logger.Info("Tracing is disabled")
	return func(ctx context.Context) error { return nil }, nil
}

// GetProviderInfo returns provider information for logging
func (n *NoOpProvider) GetProviderInfo() string {
	return "No tracing provider (disabled)"
}

// Shutdown gracefully shuts down the no-op provider (does nothing)
func (n *NoOpProvider) Shutdown(ctx context.Context) error {
	return nil
}
