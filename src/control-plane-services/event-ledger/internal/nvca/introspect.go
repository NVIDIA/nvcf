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

// Package nvca introspects NVCA's Kubernetes projected service-account token
// (PSAT) at SIS, for callers that do not hold an OpenBao-issued JWT. It wraps
// the shared nvcaintrospect.Client so Event Ledger and ReVal verify NVCA's
// identity the same way.
package nvca

import (
	"context"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/auth/nvcaintrospect"
)

// IntrospectRequest and IntrospectResult are the shared nvcaintrospect types,
// aliased here so existing callers and tests in this package are unaffected.
type IntrospectRequest = nvcaintrospect.IntrospectRequest
type IntrospectResult = nvcaintrospect.IntrospectResult

// MaxTokenSize and ErrTokenTooLarge alias the shared package's token-size limit.
const MaxTokenSize = nvcaintrospect.MaxTokenSize

var ErrTokenTooLarge = nvcaintrospect.ErrTokenTooLarge

// IsValidNVCASubject aliases the shared package's NVCA subject validator.
var IsValidNVCASubject = nvcaintrospect.IsValidNVCASubject

// Introspector verifies a bearer token by asking an external service whether
// it is currently valid. It is implemented by *Client and by test doubles.
type Introspector interface {
	Introspect(ctx context.Context, token string) (*IntrospectResult, error)
}

// Client calls SIS's POST /v1/nvca/tokens/introspect endpoint to verify
// NVCA's PSAT, using the shared nvcaintrospect.Client for the actual call,
// caching, and subject validation.
type Client struct {
	client *nvcaintrospect.Client
}

// NewClient builds an introspection client. introspectURL is required. A
// cacheTTL of 0 disables caching.
func NewClient(introspectURL string, timeout, cacheTTL time.Duration) (*Client, error) {
	client, err := nvcaintrospect.NewClient(introspectURL, timeout, cacheTTL)
	if err != nil {
		return nil, err
	}
	return &Client{client: client}, nil
}

// Introspect implements Introspector.
func (c *Client) Introspect(ctx context.Context, token string) (*IntrospectResult, error) {
	return c.client.Introspect(ctx, token)
}
