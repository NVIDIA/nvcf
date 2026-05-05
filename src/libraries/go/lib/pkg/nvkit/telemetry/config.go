/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package telemetry

import (
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const (
	defaultScope                   = "telemetry-write"
	defaultClientType              = "http"
	kratosProdEndpoint             = "https://prod.analytics.nvidiagrid.net"
	defaultKratosHTTPRetryAttempts = 2
	defaultAPIVersion              = "v2"

	defaultFlushInterval                = 5      // flushing interval for the buffer
	defaultMaxBatchSize                 = 1000   // maximum events list length per request
	defaultMaxEvents                    = 100000 // maximum buffer capacity
	defaultBaseDelayInSeconds           = 5      // base delay in seconds while retrying using exponential backoff
	defaultMaxWorkers                   = 5      // number of workers to flush
	defaultMaxBufferSize                = 100    // max size limit of task channel
	defaultRetriesForExponentialBackoff = 10
	defaultMaxRequestSizeInBytes        = 1000000 // default max request size while calling to Kratos
	defaultSyncMaxRetries               = 3
	defaultMaxBatchSizeInBytes          = 1000000 // default max batch size in bytes
	defaultSyncBatchTimeoutMs           = 0       // default timeout for sync batch
	defaultSyncBatchQueueSize           = 50      // default sync batch queue size
)

// KratosExporterConfig  consists of various config necessary for creating a KratosTelemetry Client
type KratosExporterConfig struct {
	// ClientCfg is the configuration of the base client
	ClientCfg *clients.BaseClientConfig
	// CollectorID is the ID of the kratos collector
	CollectorID string
	// NumRetries determines the number of retry attempts excluding the first request
	// zero of the value means no retry, and maximum allowed value of NumRetries is 2
	// The default value is 2, thus total attempts including the first attempt is 3.
	NumRetries int
	// FlushInterval determines the time interval in SECOND unit of the periodic flush operation when sending events asynchronously
	FlushInterval int
	// MaxBatchSize determines the maximum allowed number of events per request to be sent to the Kratos collector
	// The default value is 1000, and the goal is to decrease the number of hitting to the Kratos collector
	MaxBatchSize int
	// APIVersion specifies desired analytics API version
	APIVersion string
	// Base delay in seconds for exponential backoff retries
	BaseDelayInSeconds int
	// MaxEvents is the max number of events that the buffer can hold
	MaxEvents int
	// MaxWorkers is the number of worker go routines which are used to flush periodically
	MaxWorkers int
	// NumRetriesExponentialBackoff determines the number of retry attempts for exponential backoff
	// while sending events to Kratos
	NumRetriesExponentialBackoff int
	// MaxBufferSize determines the max buffer size of the task channel
	MaxBufferSize int
	// CustomHTTPClient allows providing a custom HTTP client to use instead of creating a new one
	CustomHTTPClient *http.Client
	// SyncMaxRetries defines the  number of retry attempts for the sync mode
	SyncMaxRetries int
	// SyncBaseDelayInSeconds defines delay in seconds for exponential backoff retries for the sync mode
	SyncBaseDelayInSeconds int
	// MaxBatchSizeInBytes defines the maximum batch size in bytes
	MaxBatchSizeInBytes int
	// SyncBatchTimeoutMs defines the timeout in ms for sync batch
	SyncBatchTimeoutMs int
	// SyncBatchQueueSize defines the initial size of the sync batch queue
	SyncBatchQueueSize int
}

// Option  provides more configuration for KratosTelemetry
type Option func(config *KratosExporterConfig) error

// WithTLS adds TLS configuration to the KratosTelemetry client
func WithTLS(cfg auth.TLSConfigOptions) Option {
	return func(k *KratosExporterConfig) error {
		k.ClientCfg.TLS = cfg
		return nil
	}
}

// WithRefreshConfig adds authn refresh configuration to the KratosTelemetry client
func WithRefreshConfig(cfg auth.RefreshConfig) Option {
	return func(k *KratosExporterConfig) error {
		k.ClientCfg.AuthnCfg.RefreshConfig = &cfg
		return nil
	}
}

// WithNumRetries configures the retry functionality of the KratosTelemetry client
func WithNumRetries(retries int) Option {
	return func(k *KratosExporterConfig) error {
		if retries < 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting with num retries", zap.Error(err))
			return err
		}
		k.NumRetries = retries
		return nil
	}
}

// WithMaxBatchSize configures the maximum number of events per request
func WithMaxBatchSize(size int) Option {
	return func(k *KratosExporterConfig) error {
		if size < 0 {
			err := errors.ErrNonPositiveValue
			zap.L().Error("error setting the max events num per request", zap.Error(err))
			return err
		}
		k.MaxBatchSize = size
		return nil
	}
}

// WithFlushInterval configures the flush interval when periodic sending the events storing in the buffer.
// the value must be larger than zero
func WithFlushInterval(interval int) Option {
	return func(k *KratosExporterConfig) error {
		if interval <= 0 {
			err := errors.ErrNonPositiveValue
			zap.L().Error("error setting the flush interval", zap.Error(err))
			return err
		}
		k.FlushInterval = interval
		return nil
	}
}

// WithMaxEvents configures the maximum size of the buffer
func WithMaxEvents(capacity int) Option {
	return func(k *KratosExporterConfig) error {
		if capacity < 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting the buffer size", zap.Error(err))
			return err
		}
		k.MaxEvents = capacity
		return nil
	}
}

// WithBaseDelayInSeconds configures the base delay while retrying using exponential backoff
func WithBaseDelayInSeconds(delay int) Option {
	return func(k *KratosExporterConfig) error {
		if delay < 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting the base delay", zap.Error(err))
			return err
		}
		k.BaseDelayInSeconds = delay
		return nil
	}
}

// WithMaxWorkers configures the number of worker go routines which are used to flush periodically
func WithMaxWorkers(count int) Option {
	return func(k *KratosExporterConfig) error {
		if count <= 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting the number of worker go routines", zap.Error(err))
			return err
		}
		k.MaxWorkers = count
		return nil
	}
}

// WithNumRetriesExponentialBackoff configures the number of worker go routines which are used to flush periodically
func WithNumRetriesExponentialBackoff(retryCount int) Option {
	return func(k *KratosExporterConfig) error {
		if retryCount <= 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting the number of retries for exponential backoff", zap.Error(err))
			return err
		}
		k.NumRetriesExponentialBackoff = retryCount
		return nil
	}
}

// WithMaxBufferSize configures the max buffer size of the task channel
func WithMaxBufferSize(bufferSize int) Option {
	return func(k *KratosExporterConfig) error {
		if bufferSize <= 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting the max buffer size", zap.Error(err))
			return err
		}
		k.MaxBufferSize = bufferSize
		return nil
	}
}

// WithSyncMaxRetries configures the maximum number of retry attempts for sync mode
func WithSyncMaxRetries(retries int) Option {
	return func(k *KratosExporterConfig) error {
		if retries < 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting sync max retries", zap.Error(err))
			return err
		}
		k.SyncMaxRetries = retries
		return nil
	}
}

// WithSyncBaseDelayInSeconds configures the base delay in seconds for exponential backoff retries in sync mode
func WithSyncBaseDelayInSeconds(delay int) Option {
	return func(k *KratosExporterConfig) error {
		if delay < 0 {
			err := errors.ErrNegativeValue
			zap.L().Error("error setting sync base delay", zap.Error(err))
			return err
		}
		k.SyncBaseDelayInSeconds = delay
		return nil
	}
}

// WithMaxBatchSizeInBytes configures the maximum batch size in bytes
func WithMaxBatchSizeInBytes(size int) Option {
	return func(k *KratosExporterConfig) error {
		if size <= 0 {
			err := errors.ErrNonPositiveValue
			zap.L().Error("error setting max batch size in bytes", zap.Error(err))
			return err
		}
		k.MaxBatchSizeInBytes = size
		return nil
	}
}

// WithSyncBatchTimeoutMs configures the timeout in milliseconds for sync batch
func WithSyncBatchTimeoutMs(timeout int) Option {
	return func(k *KratosExporterConfig) error {
		if timeout <= 0 {
			err := errors.ErrNonPositiveValue
			zap.L().Error("error setting sync batch timeout", zap.Error(err))
			return err
		}
		k.SyncBatchTimeoutMs = timeout
		return nil
	}
}

// WithSyncBatchQueueSize configures the initial size of the sync batch queue
func WithSyncBatchQueueSize(size int) Option {
	return func(k *KratosExporterConfig) error {
		if size <= 0 {
			err := errors.ErrNonPositiveValue
			zap.L().Error("error setting sync batch queue size", zap.Error(err))
			return err
		}
		k.SyncBatchQueueSize = size
		return nil
	}
}

func WithEndpoint(endpoint string) Option {
	return func(k *KratosExporterConfig) error {
		if endpoint == "" {
			k.ClientCfg.Addr = kratosProdEndpoint
		} else {
			k.ClientCfg.Addr = endpoint
		}
		return nil
	}
}

// AddCommandFlags is a helper method to add config flags to cobra command
func (cfg *KratosExporterConfig) AddCommandFlags(cmd *cobra.Command) bool {
	if cmd == nil || cfg == nil {
		return false
	}
	cmd.Flags().StringVarP(&cfg.CollectorID, "collector-id", "", "", "Kratos resource to write the telemetry data to")
	return true
}

// NewKratosExporterConfig creates a new KratosTelemetry config object with required parameters
func NewKratosExporterConfig(kratosSSAHost string, kratosClientID string, kratosClientSecret string, collectorId string, opts ...Option) (*KratosExporterConfig, error) {
	zap.L().Info("configuring kratos exporter by client id and secrets")
	cfg := &KratosExporterConfig{
		ClientCfg: &clients.BaseClientConfig{
			Type: defaultClientType,
			Addr: kratosProdEndpoint,
			TLS:  auth.TLSConfigOptions{},
			AuthnCfg: &auth.AuthnConfig{
				OIDCConfig: &auth.ProviderConfig{
					Host:         kratosSSAHost,
					ClientID:     kratosClientID,
					ClientSecret: kratosClientSecret,
					Scopes:       []string{defaultScope},
				},
				RefreshConfig: nil,
			},
		},
		CollectorID:                  collectorId,
		NumRetries:                   defaultKratosHTTPRetryAttempts,
		FlushInterval:                defaultFlushInterval,
		MaxBatchSize:                 defaultMaxBatchSize,
		APIVersion:                   defaultAPIVersion,
		BaseDelayInSeconds:           defaultBaseDelayInSeconds,
		MaxEvents:                    defaultMaxEvents,
		MaxWorkers:                   defaultMaxWorkers,
		NumRetriesExponentialBackoff: defaultRetriesForExponentialBackoff,
		MaxBufferSize:                defaultMaxBufferSize,
		SyncMaxRetries:               defaultSyncMaxRetries,
		SyncBaseDelayInSeconds:       defaultBaseDelayInSeconds,
		MaxBatchSizeInBytes:          defaultMaxBatchSizeInBytes,
		SyncBatchTimeoutMs:           defaultSyncBatchTimeoutMs,
		SyncBatchQueueSize:           defaultSyncBatchQueueSize,
	}

	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// NewKratosExporterConfigWithCredsFile creates a new KratosTelemetry config object with required parameters and cred files
func NewKratosExporterConfigWithCredentialsFile(kratosSSAHost string, credsFile string, collectorId string, opts ...Option) (*KratosExporterConfig, error) {
	zap.L().Info("configuring kratos exporter by credentials file")
	cfg := &KratosExporterConfig{
		ClientCfg: &clients.BaseClientConfig{
			Type: defaultClientType,
			Addr: kratosProdEndpoint,
			TLS:  auth.TLSConfigOptions{},
			AuthnCfg: &auth.AuthnConfig{
				OIDCConfig: &auth.ProviderConfig{
					Host:            kratosSSAHost,
					Scopes:          []string{defaultScope},
					CredentialsFile: credsFile,
				},
				RefreshConfig: nil,
			},
		},
		CollectorID:                  collectorId,
		NumRetries:                   defaultKratosHTTPRetryAttempts,
		FlushInterval:                defaultFlushInterval,
		MaxBatchSize:                 defaultMaxBatchSize,
		APIVersion:                   defaultAPIVersion,
		BaseDelayInSeconds:           defaultBaseDelayInSeconds,
		MaxEvents:                    defaultMaxEvents,
		MaxWorkers:                   defaultMaxWorkers,
		NumRetriesExponentialBackoff: defaultRetriesForExponentialBackoff,
		MaxBufferSize:                defaultMaxBufferSize,
		SyncMaxRetries:               defaultSyncMaxRetries,
		SyncBaseDelayInSeconds:       defaultBaseDelayInSeconds,
		MaxBatchSizeInBytes:          defaultMaxBatchSizeInBytes,
		SyncBatchTimeoutMs:           defaultSyncBatchTimeoutMs,
		SyncBatchQueueSize:           defaultSyncBatchQueueSize,
	}

	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}
