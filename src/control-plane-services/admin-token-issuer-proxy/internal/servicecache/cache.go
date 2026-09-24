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

package servicecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
)

const metadataErrorBodyLimit = 64 << 10

// RetryPolicy controls the exponential backoff used while api-keys is starting.
type RetryPolicy struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	OnRetry        func(error, time.Duration)
}

type fetchError struct {
	err       error
	retryable bool
}

func (e *fetchError) Error() string { return e.err.Error() }
func (e *fetchError) Unwrap() error { return e.err }

// Cache holds service metadata fetched from api-keys service
type Cache struct {
	mu          sync.RWMutex
	serviceInfo *models.ServiceInfo
	metadataURL string
	httpClient  *http.Client
}

// New creates a new service metadata cache
func New(metadataURL string) *Cache {
	return &Cache{
		metadataURL: metadataURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Fetch retrieves service metadata from the api-keys service
func (c *Cache) Fetch() error {
	return c.FetchContext(context.Background())
}

// FetchContext retrieves service metadata and honors context cancellation.
func (c *Cache) FetchContext(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.metadataURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build service metadata request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &fetchError{
			err:       fmt.Errorf("failed to fetch service metadata: %w", err),
			retryable: isRetryableTransportError(err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, metadataErrorBodyLimit))
		return &fetchError{
			err: fmt.Errorf(
				"service metadata request failed with status %d: %s",
				resp.StatusCode,
				string(body),
			),
			retryable: resp.StatusCode == http.StatusRequestTimeout ||
				resp.StatusCode == http.StatusTooManyRequests ||
				resp.StatusCode >= http.StatusInternalServerError,
		}
	}

	var servicesResp models.ServicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&servicesResp); err != nil {
		return fmt.Errorf("failed to decode service metadata: %w", err)
	}

	if len(servicesResp.Services) == 0 {
		return fmt.Errorf("no services found in response")
	}
	if servicesResp.Services[0].ServiceID == "" {
		return fmt.Errorf("service metadata is missing service_id")
	}

	// Cache the first service (typically nvcf-api)
	c.mu.Lock()
	c.serviceInfo = &servicesResp.Services[0]
	c.mu.Unlock()

	return nil
}

func isRetryableTransportError(err error) bool {
	var requestErr *url.Error
	if !errors.As(err, &requestErr) {
		return false
	}

	// Kubernetes DNS can return NXDOMAIN until a Service is created. Treat DNS
	// resolution failures as startup-transient so the process remains live and
	// unready while it waits for api-keys to become discoverable.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound || dnsErr.IsTemporary
	}

	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}

	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// FetchWithRetry fetches metadata until it succeeds, a permanent response is
// returned, or the context is canceled. Retry delays grow exponentially and
// are capped by MaxBackoff.
func (c *Cache) FetchWithRetry(ctx context.Context, policy RetryPolicy) error {
	if policy.InitialBackoff <= 0 {
		return fmt.Errorf("initial backoff must be greater than zero")
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return fmt.Errorf("maximum backoff must be at least the initial backoff")
	}

	backoff := policy.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := c.FetchContext(ctx)
		if err == nil {
			return nil
		}

		var metadataErr *fetchError
		if !errors.As(err, &metadataErr) || !metadataErr.retryable {
			return err
		}
		if policy.OnRetry != nil {
			policy.OnRetry(err, backoff)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}

		if backoff < policy.MaxBackoff/2 {
			backoff *= 2
		} else {
			backoff = policy.MaxBackoff
		}
	}
}

// Get returns the cached service info (thread-safe)
func (c *Cache) Get() *models.ServiceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serviceInfo
}

// IsReady returns true if service metadata has been fetched
func (c *Cache) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serviceInfo != nil
}
