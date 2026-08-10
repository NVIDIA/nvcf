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

package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"

	"github.com/hashicorp/go-retryablehttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

// Config contains Webhook plugin configuration.
type Config struct {
	URL                string        `mapstructure:"url"                  yaml:"url"`
	Timeout            time.Duration `mapstructure:"timeout"              yaml:"timeout"`
	RetryAttempts      int           `mapstructure:"retry_attempts"       yaml:"retry_attempts"`
	InsecureSkipVerify bool          `mapstructure:"insecure_skip_verify" yaml:"insecure_skip_verify"`
}

// Plugin implements webhook-based authentication.
type Plugin struct {
	config      *Config
	retryClient *retryablehttp.Client
	logger      *zap.Logger
}

// Request represents the request sent to webhook endpoint.
type Request struct {
	Account    string `json:"account"`
	PluginName string `json:"pluginName"`
	Payload    string `json:"payload"`
}

// Response represents the response from webhook endpoint.
type Response struct {
	UserID      string             `json:"userId"`
	Account     string             `json:"account"`
	Permissions *types.Permissions `json:"permissions,omitempty"`
	Error       string             `json:"error,omitempty"`
	Claims      map[string]any     `json:"claims,omitempty"`
	TTL         time.Duration      `json:"ttl,omitempty"`
}

// NewPlugin creates a new webhook plugin instance.
func NewPlugin(configData any, logger *zap.Logger) (*Plugin, error) {
	// Parse configuration
	webhookConfig, err := parseWebhookConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse webhook config: %w", err)
	}

	retryClient := retryablehttp.NewClient()

	retryClient.RetryMax = webhookConfig.RetryAttempts
	retryClient.RetryWaitMin = 0
	retryClient.RetryWaitMax = 3 * time.Second

	transport := &http.Transport{
		MaxIdleConns:    10,
		IdleConnTimeout: 90 * time.Second,
	}

	// Configure TLS if needed
	if webhookConfig.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // Configurable for development/testing
		}
	}

	// Wrap transport with OpenTelemetry instrumentation
	instrumentedTransport := otelhttp.NewTransport(transport)

	retryClient.HTTPClient = &http.Client{
		Timeout:   webhookConfig.Timeout,
		Transport: instrumentedTransport,
	}

	retryClient.Logger = &leveledLoggerAdapter{logger}

	plugin := &Plugin{
		config:      webhookConfig,
		retryClient: retryClient,
		logger:      logger.With(zap.String("plugin", "webhook")),
	}

	return plugin, nil
}

// Authenticate validates the request using webhook and returns auth result.
func (p *Plugin) Authenticate(ctx context.Context, req *types.Request) (*types.Result, error) {
	// Call webhook with retries
	authResult, err := p.callWebhook(ctx, req.Account, req.PluginName, req.Payload)
	if err != nil {
		return nil, err
	}

	return authResult, nil
}

// callWebhook calls the webhook endpoint with retries.
func (p *Plugin) callWebhook(ctx context.Context, account, pluginName, payload string) (*types.Result, error) {
	p.logger.Info("Performing webhook authentication",
		zap.String("account", account),
		zap.String("plugin_name", pluginName),
		zap.String("url", p.config.URL))

	webhookReq := Request{
		Account:    account,
		PluginName: pluginName,
		Payload:    payload,
	}

	reqBody, err := json.Marshal(webhookReq)
	if err != nil {
		// untested section
		return nil, types.NewAuthError(types.ErrTypeInternalError, "failed to marshal webhook request", 500)
	}

	// Create retryable request
	retryReq, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPost, p.config.URL, bytes.NewReader(reqBody))
	if err != nil {
		// untested section
		return nil, types.NewAuthError(types.ErrTypeInternalError, "failed to create webhook request", 500)
	}

	// Set headers
	retryReq.Header.Set("Content-Type", "application/json")
	retryReq.Header.Set("User-Agent", "auth-callout-service/1.0")

	// Execute request with retries
	resp, err := p.retryClient.Do(retryReq)
	if err != nil {
		// untested: network-level failures are complex to test reliably
		return nil, types.NewAuthError(types.ErrTypeInternalError, fmt.Sprintf("webhook request failed: %v", err), 500)
	}
	defer resp.Body.Close()

	// Handle non-2xx responses - convert to appropriate auth errors
	if resp.StatusCode >= 400 {
		return nil, p.handleErrorResponse(resp)
	}

	return p.parseSuccessResponse(resp)
}

// handleErrorResponse handles non-2xx HTTP responses and converts them to appropriate auth errors.
func (p *Plugin) handleErrorResponse(resp *http.Response) error {
	// 4xx client errors - categorize by retryability
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			// Authentication/authorization errors - don't retry
			p.logger.Warn("Authentication denied",
				zap.Int("status_code", resp.StatusCode),
				zap.String("url", p.config.URL))
			return types.NewAuthError(types.ErrTypeUnauthorized, "webhook authentication failed", resp.StatusCode)
		case http.StatusRequestTimeout:
			// Request timeout - can retry
			p.logger.Warn("Webhook request timeout",
				zap.Int("status_code", resp.StatusCode),
				zap.String("url", p.config.URL))
			return types.NewAuthError(types.ErrTypeInternalError, "webhook request timeout", resp.StatusCode)
		case http.StatusTooManyRequests:
			// Rate limiting - can retry with backoff
			p.logger.Warn("Webhook rate limited",
				zap.Int("status_code", resp.StatusCode),
				zap.String("url", p.config.URL))
			return types.NewAuthError(types.ErrTypeRateLimit, "webhook rate limited", resp.StatusCode)
		default:
			// All other 4xx errors are permanent client errors - don't retry
			p.logger.Warn("Invalid authentication response",
				zap.Int("status_code", resp.StatusCode),
				zap.String("url", p.config.URL))
			return types.NewAuthError(types.ErrTypeInvalidRequest, fmt.Sprintf("webhook client error: %d", resp.StatusCode), resp.StatusCode)
		}
	}
	// 5xx server errors - can be retried
	p.logger.Error("Request failed with status",
		zap.Int("status_code", resp.StatusCode),
		zap.String("url", p.config.URL))
	return types.NewAuthError(types.ErrTypeInternalError, fmt.Sprintf("webhook service error: %d", resp.StatusCode), resp.StatusCode)
}

func (p *Plugin) parseSuccessResponse(resp *http.Response) (*types.Result, error) {
	// Parse response
	var webhookResp Response
	if err := json.NewDecoder(resp.Body).Decode(&webhookResp); err != nil {
		return nil, types.NewAuthError(types.ErrTypeInternalError, "failed to decode webhook response", 500)
	}

	// Validate required fields
	if webhookResp.UserID == "" {
		return nil, types.NewAuthError(types.ErrTypeInternalError, "webhook response missing userId", 500)
	}

	authResult := &types.Result{
		UserID:      webhookResp.UserID,
		Account:     webhookResp.Account,
		Permissions: webhookResp.Permissions,
		TTL:         webhookResp.TTL,
	}

	return authResult, nil
}

// parseWebhookConfig parses webhook plugin configuration.
func parseWebhookConfig(configData any) (*Config, error) {
	webhookConfig := &Config{}
	if err := config.DecodeConfig(configData, webhookConfig); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	if webhookConfig.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	// Set defaults
	if webhookConfig.Timeout == 0 {
		webhookConfig.Timeout = 30 * time.Second
	}
	if webhookConfig.RetryAttempts == 0 {
		webhookConfig.RetryAttempts = 3
	}

	return webhookConfig, nil
}

// leveledLoggerAdapter adapts zap.Logger to retryablehttp.Logger interface.
type leveledLoggerAdapter struct {
	*zap.Logger
}

func (l *leveledLoggerAdapter) Error(msg string, keysAndValues ...any) {
	l.Logger.Error(msg, mapKvToZapFields(keysAndValues...)...)
}

func (l *leveledLoggerAdapter) Info(msg string, keysAndValues ...any) {
	l.Logger.Info(msg, mapKvToZapFields(keysAndValues...)...)
}

func (l *leveledLoggerAdapter) Debug(msg string, keysAndValues ...any) {
	l.Logger.Debug(msg, mapKvToZapFields(keysAndValues...)...)
}

func (l *leveledLoggerAdapter) Warn(msg string, keysAndValues ...any) {
	l.Logger.Warn(msg, mapKvToZapFields(keysAndValues...)...)
}

// mapKvToZapFields converts key-value pairs to zap fields.
func mapKvToZapFields(keysAndValues ...any) []zap.Field {
	if len(keysAndValues)%2 != 0 {
		return []zap.Field{zap.Any("invalid_kv_pairs", keysAndValues)}
	}

	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues); i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			key = fmt.Sprintf("key_%d", i/2)
		}
		fields = append(fields, zap.Any(key, keysAndValues[i+1]))
	}
	return fields
}
