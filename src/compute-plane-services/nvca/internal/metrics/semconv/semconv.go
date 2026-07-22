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

// Package semconv holds attribute keys and helpers shared by the per-transport
// semconv families (httpsemconv, msgsemconv, rpcsemconv). It exposes the small
// set of attributes NVCA adds on top of the OpenTelemetry Semantic Conventions,
// notably peer.service and error.type, so that every outbound-client instrument
// uses one consistent, bounded label vocabulary.
package semconv

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"

	"go.opentelemetry.io/otel/attribute"
)

const (
	// PeerServiceKey identifies the logical dependency a call targets
	// (for example "icms", "sis", "reval", "ngc"). NVCA-added; it is the
	// primary grouping key for dashboards.
	PeerServiceKey = attribute.Key("peer.service")

	// ErrorTypeKey is the semconv error.type attribute, set only when a call
	// fails. It replaces NVCA's historical synthetic http_status="0".
	ErrorTypeKey = attribute.Key("error.type")
)

// Error-type values are a bounded classification of transport-level failures,
// used when there is no HTTP/RPC status code to report.
const (
	ErrorTypeTimeout           = "timeout"
	ErrorTypeConnectionRefused = "connection_refused"
	ErrorTypeCanceled          = "canceled"
	ErrorTypeOther             = "other"
)

// PeerService returns the peer.service attribute for the given dependency name.
func PeerService(name string) attribute.KeyValue {
	return PeerServiceKey.String(name)
}

// ErrorType returns the error.type attribute for the given value.
func ErrorType(value string) attribute.KeyValue {
	return ErrorTypeKey.String(value)
}

// ClassifyError maps an error to a bounded error.type value. It returns an
// empty string for a nil error so callers can omit the attribute on success.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return ErrorTypeTimeout
	case errors.Is(err, context.Canceled):
		return ErrorTypeCanceled
	case errors.Is(err, syscall.ECONNREFUSED):
		return ErrorTypeConnectionRefused
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTypeTimeout
	}
	return ErrorTypeOther
}
