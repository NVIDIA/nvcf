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

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"syscall"
	"time"
)

// APIError preserves the HTTP status of an unsuccessful API response.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf(apiErrorFormat, e.StatusCode, e.Body)
}

// WaitForFunctionDeployment waits for ACTIVE while tolerating transient status query failures.
func (c *Client) WaitForFunctionDeployment(ctx context.Context, functionID, versionID string, timeoutSec int) error {
	return c.waitForFunctionDeployment(ctx, functionID, versionID, time.Duration(timeoutSec)*time.Second, 10*time.Second, time.Second)
}

func (c *Client) waitForFunctionDeployment(parent context.Context, functionID, versionID string, timeout, pollInterval, retryDelay time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var lastQueryError error
	retries := 0
	waitError := func() error {
		if parent.Err() != nil {
			return parent.Err()
		}
		if lastQueryError != nil {
			return fmt.Errorf("deployment status could not be confirmed within %s (last query failed: %v): %w", timeout, lastQueryError, ctx.Err())
		}
		return fmt.Errorf("function deployment timed out after %s: %w", timeout, ctx.Err())
	}
	for {
		if ctx.Err() != nil {
			return waitError()
		}
		resp, err := c.GetFunction(ctx, functionID, versionID)
		lastQueryError = err
		if ctx.Err() != nil {
			return waitError()
		}
		delay := pollInterval
		if err != nil {
			if !retryableStatusQuery(err) || retries == 3 {
				return fmt.Errorf("could not query function deployment status: %w", err)
			}
			delay = retryDelay * time.Duration(1<<retries)
			retries++
			// Response bodies can contain user data. Log identity and retry count only.
			log.Printf("WARN: Retrying deployment status query function_id=%s version_id=%s retry=%d delay=%s", functionID, versionID, retries, delay)
		} else {
			retries = 0
			lastQueryError = nil
			if c.config.Debug {
				log.Printf("DEBUG: Function status function_id=%s version_id=%s status=%s", functionID, versionID, resp.Function.Status)
			}
			switch resp.Function.Status {
			case "ACTIVE":
				return nil
			case "ERROR", "FAILED":
				return fmt.Errorf("function deployment failed with status: %s", resp.Function.Status)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return waitError()
		case <-timer.C:
		}
	}
}

func retryableStatusQuery(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusBadGateway || apiErr.StatusCode == http.StatusServiceUnavailable || apiErr.StatusCode == http.StatusGatewayTimeout
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
