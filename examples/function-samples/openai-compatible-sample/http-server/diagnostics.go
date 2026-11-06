// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

// Request diagnostics for the Messages route.
//
// The x_load_tester_echo_diagnostics body control makes the backend report which
// replica served the request and which headers it received, so a client can
// check replica affinity and which caller headers a gateway forwarded. It is off
// by default and only affects responses that ask for it. The control is a body
// field rather than a header because any x-load-tester-* header disables every
// body control.

const (
	replicaHeader         = "x-backend-replica"
	functionVersionHeader = "x-backend-function-version"
	logRequestsEnv        = "LOG_REQUESTS"
	redactedValue         = "<redacted>"
)

// logRequests logs one line per Messages request (method, path, request id,
// replica and received header names; never header values) when LOG_REQUESTS=true.
var logRequests = os.Getenv(logRequestsEnv) == "true"

type backendDiagnostics struct {
	Replica           string              `json:"replica"`
	FunctionID        string              `json:"function_id,omitempty"`
	FunctionVersionID string              `json:"function_version_id,omitempty"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	Query             string              `json:"query,omitempty"`
	Host              string              `json:"host,omitempty"`
	RemoteAddr        string              `json:"remote_addr,omitempty"`
	ContentLength     int64               `json:"content_length"`
	Headers           map[string][]string `json:"received_headers"`
}

// replicaID identifies the serving instance: the NVCF instance id (the pod
// name), falling back to the hostname outside NVCF.
func replicaID() string {
	if id := os.Getenv("NVCF_INSTANCE_ID"); id != "" {
		return id
	}
	if host, err := os.Hostname(); err == nil {
		return host
	}
	return "unknown"
}

func newBackendDiagnostics(r *http.Request) *backendDiagnostics {
	headers := make(map[string][]string, len(r.Header))
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "authorization", "proxy-authorization", "x-api-key", "cookie":
			headers[lower] = []string{redactedValue}
		default:
			headers[lower] = append([]string(nil), values...)
		}
	}
	return &backendDiagnostics{
		Replica:           replicaID(),
		FunctionID:        os.Getenv("NVCF_FUNCTION_ID"),
		FunctionVersionID: os.Getenv("NVCF_FUNCTION_VERSION_ID"),
		Method:            r.Method,
		Path:              r.URL.Path,
		Query:             r.URL.RawQuery,
		Host:              r.Host,
		RemoteAddr:        r.RemoteAddr,
		ContentLength:     r.ContentLength,
		Headers:           headers,
	}
}

// setDiagnosticHeaders puts the short identifiers on the response headers so they
// are readable for streamed responses too. Call before the response is written.
func setDiagnosticHeaders(w http.ResponseWriter, diagnostics *backendDiagnostics) {
	w.Header().Set(replicaHeader, diagnostics.Replica)
	if diagnostics.FunctionVersionID != "" {
		w.Header().Set(functionVersionHeader, diagnostics.FunctionVersionID)
	}
}

func logMessagesRequest(r *http.Request, requestID string) {
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	log.Printf("messages request id=%s replica=%s method=%s path=%s headers=%s",
		requestID, replicaID(), r.Method, r.URL.Path, strings.Join(names, ","))
}
