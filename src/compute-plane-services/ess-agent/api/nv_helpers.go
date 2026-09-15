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
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/go-hclog"
)

const HttpHeaderNameEssAgentID = "X-ESS-Agent-ID"
const EnvEssAgentLocaldev = "ESS_AGENT_LOCALDEV"
const HttpHeaderVaultToken = "X-Vault-Token"
const HttpHeaderVaultNamespace = "X-Vault-Namespace"

// IsLocalDev checks if the specified localdev environment variable is set
func IsLocalDev() bool {
	return strings.ToLower(os.Getenv(EnvEssAgentLocaldev)) == "true"
}

// ShimVaultTokenHeader sets X-Vault-Token header if running from local development environment
func ShimVaultTokenHeader(req *http.Request, token string) {
	if req == nil || req.Header == nil {
		return
	}
	if IsLocalDev() {
		req.Header.Set(HttpHeaderVaultToken, token)
	}
}

// ShimVaultNamespaceHeader sets X-Vault-Namespace header if running from local development environment
func ShimVaultNamespaceHeader(req *Request, token string) {
	if req == nil || req.Headers == nil {
		return
	}
	if IsLocalDev() {
		req.Headers.Set(HttpHeaderVaultNamespace, token)
	}
}

// LogRequestId logs ESS agent requests
func LogRequestId(ns string, resp *Response, logger hclog.Logger) {
	if resp != nil && resp.Response.Request.Header.Get(HttpHeaderNameEssAgentID) != "" {
		namespace := ns
		if namespace == "" {
			namespace = "root"
		}

		logger.Info(fmt.Sprintf("ESS request | ESS Agent Id: %s | Namespace: %s | URL: %s %s | Code: %d | Request Id: %s",
			resp.Response.Request.Header.Get(HttpHeaderNameEssAgentID),
			namespace,
			resp.Request.Method,
			resp.Request.URL.String(),
			resp.StatusCode,
			resp.Header.Get(HttpHeaderNameEssRequestId)),
		)
	}
}
