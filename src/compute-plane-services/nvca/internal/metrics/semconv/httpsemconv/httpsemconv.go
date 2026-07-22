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

// Package httpsemconv builds the OpenTelemetry Semantic Conventions attribute
// set for outbound HTTP client calls. It returns a bounded, low-cardinality
// label set so the caller does not hand-spell attribute keys.
package httpsemconv

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv"
)

// Attribute keys follow https://opentelemetry.io/docs/specs/semconv/http/http-metrics/
const (
	RequestMethodKey      = attribute.Key("http.request.method")
	ResponseStatusCodeKey = attribute.Key("http.response.status_code")
	ServerAddressKey      = attribute.Key("server.address")
	// URLTemplateKey is the route shape (for example /v2/.../heartbeat), never
	// the raw URL. It is opt-in: callers set it only where a safe, low-cardinality
	// template is known, since a generic transport cannot infer it.
	URLTemplateKey = attribute.Key("url.template")
)

// ClientAttrs returns the semconv attribute set for one outbound HTTP client
// call. peerService and method are always included. statusCode is included when
// greater than zero (a response was received); urlTemplate and errType are
// included only when non-empty, so successful calls carry no error.type and
// transport failures carry no status code.
func ClientAttrs(peerService, method, serverAddress, urlTemplate string, statusCode int, errType string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 6)
	if peerService != "" {
		attrs = append(attrs, semconv.PeerService(peerService))
	}
	if method != "" {
		attrs = append(attrs, RequestMethodKey.String(method))
	}
	if serverAddress != "" {
		attrs = append(attrs, ServerAddressKey.String(serverAddress))
	}
	if urlTemplate != "" {
		attrs = append(attrs, URLTemplateKey.String(urlTemplate))
	}
	if statusCode > 0 {
		attrs = append(attrs, ResponseStatusCodeKey.Int(statusCode))
	}
	if errType != "" {
		attrs = append(attrs, semconv.ErrorType(errType))
	}
	return attrs
}
