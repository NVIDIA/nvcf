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

package config

import (
	"net/http"
	"time"
)

// NewHTTPTransport creates a new http.Transport with the configured settings
func (h HTTPClientConfig) NewHTTPTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:          h.MaxIdleConns,
		MaxIdleConnsPerHost:   h.MaxIdleConnsPerHost,
		IdleConnTimeout:       time.Duration(h.IdleConnTimeoutSec) * time.Second,
		TLSHandshakeTimeout:   time.Duration(h.TLSHandshakeTimeoutSec) * time.Second,
		ExpectContinueTimeout: time.Duration(h.ExpectContinueTimeoutSec) * time.Second,
	}
}

// DefaultHTTPConfig returns a HTTPClientConfig with sensible defaults for JWT/JWKS operations
func DefaultHTTPConfig() HTTPClientConfig {
	return HTTPClientConfig{
		MaxIdleConns:             100,
		MaxIdleConnsPerHost:      100,
		IdleConnTimeoutSec:       90,
		TLSHandshakeTimeoutSec:   10,
		ExpectContinueTimeoutSec: 1,
	}
}
