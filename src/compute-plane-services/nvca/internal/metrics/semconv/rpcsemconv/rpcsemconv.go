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

// Package rpcsemconv builds the OpenTelemetry Semantic Conventions attribute
// set for RPC client calls. It is scaffolded for future use; NVCA has no gRPC
// client today, so nothing consumes it yet.
package rpcsemconv

import (
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv"
)

// Attribute keys follow https://opentelemetry.io/docs/specs/semconv/rpc/rpc-metrics/
const (
	SystemKey         = attribute.Key("rpc.system")
	MethodKey         = attribute.Key("rpc.method")
	GRPCStatusCodeKey = attribute.Key("rpc.grpc.status_code")
	HTTPStatusCodeKey = attribute.Key("rpc.http.status_code")
)

// Known rpc.system values.
const (
	SystemGRPC = "grpc"
)

// ClientAttrs returns the semconv attribute set for one RPC client call.
// grpcStatusCode is included when non-negative; errType only on failure.
func ClientAttrs(peerService, system, method string, grpcStatusCode int, errType string) []attribute.KeyValue {
	return clientAttrs(peerService, system, method, GRPCStatusCodeKey, grpcStatusCode, errType)
}

// ClientAttrsHTTP is the equivalent for an RPC system carried over HTTP, where
// semconv reports the transport status as rpc.http.status_code rather than
// rpc.grpc.status_code. httpStatusCode is included when non-negative.
func ClientAttrsHTTP(peerService, system, method string, httpStatusCode int, errType string) []attribute.KeyValue {
	return clientAttrs(peerService, system, method, HTTPStatusCodeKey, httpStatusCode, errType)
}

func clientAttrs(
	peerService, system, method string,
	statusKey attribute.Key, statusCode int,
	errType string,
) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 5)
	if peerService != "" {
		attrs = append(attrs, semconv.PeerService(peerService))
	}
	if system != "" {
		attrs = append(attrs, SystemKey.String(system))
	}
	if method != "" {
		attrs = append(attrs, MethodKey.String(method))
	}
	if statusCode >= 0 {
		attrs = append(attrs, statusKey.Int(statusCode))
	}
	if errType != "" {
		attrs = append(attrs, semconv.ErrorType(errType))
	}
	return attrs
}
