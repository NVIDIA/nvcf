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
	"strings"

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

// MethodOther is the semconv placeholder recorded when a request method is not
// one of the known values. It keeps http.request.method a closed set: a
// malformed or unexpected method cannot mint a new time series.
const MethodOther = "_OTHER"

// knownMethods is the semconv set of well-known HTTP methods.
// See https://opentelemetry.io/docs/specs/semconv/registry/attributes/http/
var knownMethods = map[string]struct{}{
	"CONNECT": {}, "DELETE": {}, "GET": {}, "HEAD": {}, "OPTIONS": {},
	"PATCH": {}, "POST": {}, "PUT": {}, "QUERY": {}, "TRACE": {},
}

// NormalizeMethod canonicalises a request method for http.request.method. A
// known method is upper-cased to its canonical form; anything else becomes
// MethodOther. An empty method stays empty so the attribute is omitted.
func NormalizeMethod(method string) string {
	if method == "" {
		return ""
	}
	canonical := strings.ToUpper(method)
	if _, ok := knownMethods[canonical]; ok {
		return canonical
	}
	return MethodOther
}

// ClientAttrs returns the semconv attribute set for one outbound HTTP client
// call. Every attribute is omitted when its value is empty or unset:
// statusCode is included when greater than zero (a response was received), and
// peerService, method, serverAddress, urlTemplate and errType when non-empty. So
// a successful call carries no error.type and a transport failure no status
// code. method is normalised through NormalizeMethod to keep it a closed set.
func ClientAttrs(peerService, method, serverAddress, urlTemplate string, statusCode int, errType string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 6)
	if peerService != "" {
		attrs = append(attrs, semconv.PeerService(peerService))
	}
	if m := NormalizeMethod(method); m != "" {
		attrs = append(attrs, RequestMethodKey.String(m))
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
