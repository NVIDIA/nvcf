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

package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestLogRequestId(t *testing.T) {
	tests := map[string]struct {
		reqMethod      string
		respBody       []byte
		reqURL         string
		respStatusCode int
		expcetedLog    string
	}{
		"ess: success log": {
			reqMethod:      "GET",
			respBody:       []byte(`success`),
			reqURL:         "http://ess.agent.com/v1/testpath",
			respStatusCode: http.StatusOK,
			expcetedLog:    "ESS request | ESS Agent Id: 987654321 | Namespace: test | URL: GET http://ess.agent.com/v1/testpath | Code: 200 | Request Id: 123456",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var logOutput bytes.Buffer

			// Create a logger with the logOutput buffer
			logger := hclog.New(&hclog.LoggerOptions{
				Name:   "test",
				Output: &logOutput,
				Level:  hclog.Info,
			})

			resp := createTestResponse(tt.respStatusCode, string(tt.respBody), tt.reqMethod, tt.reqURL)
			resp.Response.Request.Header.Add(HttpHeaderNameEssAgentID, "987654321")
			LogRequestId("test", &resp, logger)
			require.Contains(t, logOutput.String(), tt.expcetedLog)
		})
	}
}
