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

package health

import (
	"net/http"
	"sync/atomic"
)

const (
	DefaultUnauthorizedFailureThreshold uint64 = 3
)

// TokenFetcherHealthCheck implements both TokenFetcher's listener interface
// and the healthchecker interface to be used as a liveness probe
type TokenFetcherHealthCheck struct {
	name                         string
	unauthorizedFailedCounter    *atomic.Uint64
	unauthorizedFailureThreshold uint64
	onFetchTokenResponseFunc     func(int)
	statusOKFunc                 func() bool
}

type TokenFetcherHealthCheckOption func(*TokenFetcherHealthCheck)

func WithUnauthorizedFailureThreshold(unauthorizedFailureThreshold uint64) TokenFetcherHealthCheckOption {
	return func(tfhc *TokenFetcherHealthCheck) {
		tfhc.unauthorizedFailureThreshold = unauthorizedFailureThreshold
	}
}

// SuccessfulTokenFetcherHealthCheck returns a HealthCheck that always returns successful
func SuccessfulTokenFetcherHealthCheck(name string) *TokenFetcherHealthCheck {
	return NewTokenFetcherHealthCheck(name, func(c *TokenFetcherHealthCheck) {
		c.onFetchTokenResponseFunc = func(int) {}
		c.statusOKFunc = func() bool {
			return true
		}
	})
}

func NewTokenFetcherHealthCheck(name string, opts ...TokenFetcherHealthCheckOption) *TokenFetcherHealthCheck {
	healthCheck := &TokenFetcherHealthCheck{
		name:                         name,
		unauthorizedFailedCounter:    &atomic.Uint64{},
		unauthorizedFailureThreshold: DefaultUnauthorizedFailureThreshold,
	}
	healthCheck.onFetchTokenResponseFunc = healthCheck.onFetchTokenResponse
	healthCheck.statusOKFunc = healthCheck.statusOK
	for _, o := range opts {
		o(healthCheck)
	}
	return healthCheck
}

func (c *TokenFetcherHealthCheck) OnFetchTokenResponse(respStatusCode int) {
	c.onFetchTokenResponseFunc(respStatusCode)
}

func (c *TokenFetcherHealthCheck) onFetchTokenResponse(respStatusCode int) {
	// Increment counter 1 if we receive a 401
	// for now only check 401 and nothing else due to the concern
	// that a 429 or 503 (temporary stafleet outage) for example
	// could cause a cascading restart of NVCA instances around
	// the globe. Better to error on the side of only 401 for now
	// and revisiting if we have additional bad codes.
	if respStatusCode == http.StatusUnauthorized {
		c.unauthorizedFailedCounter.Add(1)
	} else if respStatusCode == http.StatusOK {
		// Reset the counter if we receive a 200
		c.unauthorizedFailedCounter.Store(0)
	}
}

func (c *TokenFetcherHealthCheck) StatusOK() bool {
	return c.statusOKFunc()
}

func (c *TokenFetcherHealthCheck) statusOK() bool {
	return c.unauthorizedFailedCounter.Load() <= c.unauthorizedFailureThreshold
}

func (c *TokenFetcherHealthCheck) Name() string { return c.name }
