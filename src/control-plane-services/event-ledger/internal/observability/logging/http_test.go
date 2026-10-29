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
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeHeadersRedactsSensitiveHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization":               []string{"Bearer secret"},
		"Cookie":                      []string{"session=secret"},
		"Proxy-Authorization":         []string{"Basic secret"},
		"Set-Cookie":                  []string{"session=secret"},
		"Grpc-Metadata-Authorization": []string{"Bearer grpc-secret"},
		"X-Api-Key":                   []string{"api-secret"},
		"X-ApiKey":                    []string{"api-secret"},
		"X-Session-Id":                []string{"session-secret"},
		"X-Trace-Id":                  []string{"trace-1"},
	}

	sanitized := sanitizeHeaders(headers)

	assert.Equal(t, []string{redactedHeaderValue}, sanitized["Authorization"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["Cookie"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["Proxy-Authorization"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["Set-Cookie"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["Grpc-Metadata-Authorization"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["X-Api-Key"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["X-ApiKey"])
	assert.Equal(t, []string{redactedHeaderValue}, sanitized["X-Session-Id"])
	assert.Equal(t, []string{"trace-1"}, sanitized["X-Trace-Id"])
	assert.Equal(t, []string{"Bearer secret"}, headers["Authorization"])
}

func TestRequestURLForLoggingDropsQueryAndFragment(t *testing.T) {
	requestURL, err := url.Parse("https://api.test/v1/ledger?token=secret&email=user@example.com#fragment")
	require.NoError(t, err)

	assert.Equal(t, "https://api.test/v1/ledger", requestURLForLogging(requestURL))
}
