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

package mcp

import (
	"net/url"
	"strings"
)

func IsInternalConnector(serverURL string) bool {
	if serverURL == "" {
		return false
	}

	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return false
	}

	return strings.Contains(parsedURL.Host, "mcp-connectors") ||
		strings.Contains(parsedURL.Host, ".svc.cluster.local")
}

func RedactServerURL(serverURL string) string {
	if serverURL == "" {
		return serverURL
	}

	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}

	if IsInternalConnector(serverURL) {
		return serverURL
	}

	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return serverURL
	}

	base := parsedURL.Scheme + "://" + parsedURL.Host
	switch {
	case parsedURL.Path != "" && parsedURL.Path != "/":
		return base + "/<redacted>"
	case parsedURL.RawQuery != "":
		return base + "/?<redacted>"
	default:
		if parsedURL.Path == "/" {
			return base + "/"
		}
		return base
	}
}
