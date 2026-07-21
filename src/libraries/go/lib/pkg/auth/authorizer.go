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

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Common authorization errors.
var (
	ErrUnauthorized       = errors.New("unauthorized: missing or invalid authentication credentials")
	ErrForbidden          = errors.New("forbidden: principal lacks permission for the requested action")
	ErrAuthorizerInternal = errors.New("internal authorizer error")
)

// maxWebhookResponseBytes limits how many bytes are read from a webhook response body.
// This prevents a faulty or hostile endpoint from consuming unbounded memory.
const maxWebhookResponseBytes = 4096

// Action represents the permission or operation being requested.
// Consuming services should define their own Action constants in their own packages.
type Action string

// AuthRequest encapsulates the identity, action, and target resource for an authorization check.
type AuthRequest struct {
	// Credential holds the token or secret presented by the client.
	Credential string `json:"credential,omitempty"`
	// OrgID identifies the organization or tenant context.
	OrgID string `json:"org_id,omitempty"`
	// PrincipalID identifies the user or service principal requesting access.
	PrincipalID string `json:"principal_id,omitempty"`
	// ResourceID identifies the target resource being accessed.
	ResourceID string `json:"resource_id,omitempty"`
	// Action specifies the operation requested on the resource.
	Action Action `json:"action"`
}

// AuthResult represents the outcome of an authorization check.
type AuthResult struct {
	// Allowed indicates whether the requested action is permitted.
	Allowed bool `json:"allowed"`
	// PrincipalID is the verified identity of the requester.
	PrincipalID string `json:"principal_id,omitempty"`
	// Scopes lists the granted permission scopes or roles.
	Scopes []string `json:"scopes,omitempty"`
	// Reason provides human-readable context when access is denied.
	Reason string `json:"reason,omitempty"`
}

// Authorizer defines the public interface for evaluating access requests in open-source builds.
type Authorizer interface {
	// Authorize evaluates whether the request is allowed to perform the action.
	Authorize(ctx context.Context, req *AuthRequest) (*AuthResult, error)
}

// NoopAuthorizer implements Authorizer by allowing all requests.
// This implementation is intended for local development and standalone testing.
type NoopAuthorizer struct{}

// NewNoopAuthorizer creates a new NoopAuthorizer that allows every request.
func NewNoopAuthorizer() *NoopAuthorizer {
	return &NoopAuthorizer{}
}

// Authorize always returns an allowed result.
func (a *NoopAuthorizer) Authorize(ctx context.Context, req *AuthRequest) (*AuthResult, error) {
	if req == nil {
		return nil, ErrUnauthorized
	}
	principal := req.PrincipalID
	if principal == "" {
		principal = "anonymous"
	}
	return &AuthResult{
		Allowed:     true,
		PrincipalID: principal,
		Scopes:      []string{"*"},
	}, nil
}

// noRedirectPolicy is an http.Client.CheckRedirect function that refuses all redirects.
// This prevents 307/308 responses from replaying a POST carrying AuthRequest.Credential
// to an unintended redirect target.
func noRedirectPolicy(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// WebhookAuthorizer implements Authorizer by delegating access evaluation to an external HTTP service.
type WebhookAuthorizer struct {
	client      *http.Client
	endpointURL string
}

// WebhookOption configures optional parameters for WebhookAuthorizer.
type WebhookOption func(*WebhookAuthorizer)

// WithHTTPClient sets a custom HTTP client for the webhook authorizer.
// The provided client must not be nil.
func WithHTTPClient(client *http.Client) WebhookOption {
	return func(w *WebhookAuthorizer) {
		w.client = client
	}
}

// NewWebhookAuthorizer initializes a WebhookAuthorizer targeting the provided endpoint URL.
// It returns an error if endpointURL is empty, any option is nil, or the resolved HTTP client is nil.
// The default HTTP client is instrumented with OpenTelemetry and configured to refuse redirects.
func NewWebhookAuthorizer(endpointURL string, opts ...WebhookOption) (*WebhookAuthorizer, error) {
	if endpointURL == "" {
		return nil, errors.New("webhook endpoint URL must not be empty")
	}
	defaultClient := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: noRedirectPolicy,
	}
	defaultClient.Transport = otelhttp.NewTransport(defaultClient.Transport)
	w := &WebhookAuthorizer{
		endpointURL: endpointURL,
		client:      defaultClient,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("webhook option must not be nil")
		}
		opt(w)
	}
	if w.client == nil {
		return nil, errors.New("webhook HTTP client must not be nil")
	}
	// Enforce no-redirect policy on injected clients that do not set one.
	if w.client.CheckRedirect == nil {
		w.client.CheckRedirect = noRedirectPolicy
	}
	return w, nil
}

// Authorize posts the authorization request to the webhook endpoint and decodes the result.
// It propagates the W3C Trace Context from ctx via the OTEL-instrumented transport and records
// transport errors and non-OK HTTP responses on the resulting span.
func (w *WebhookAuthorizer) Authorize(ctx context.Context, req *AuthRequest) (*AuthResult, error) {
	if req == nil {
		return nil, ErrUnauthorized
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal auth request: %w", ErrAuthorizerInternal, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpointURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create HTTP request: %w", ErrAuthorizerInternal, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: webhook request failed: %w", ErrAuthorizerInternal, err)
	}
	defer resp.Body.Close()

	// Limit body reads to maxWebhookResponseBytes to guard against unbounded memory use
	// from a faulty or hostile webhook endpoint.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read webhook response body: %w", ErrAuthorizerInternal, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusForbidden {
		reason := "forbidden by policy"
		if len(respBody) > 0 {
			reason = string(respBody)
		}
		return &AuthResult{Allowed: false, Reason: reason}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d from webhook: %s", ErrAuthorizerInternal, resp.StatusCode, respBody)
	}

	var result AuthResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("%w: failed to decode webhook response: %w", ErrAuthorizerInternal, err)
	}

	return &result, nil
}
