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

// Mock backend for the NVCF admin endpoints.
//
// Serves canned responses for the six admin endpoints exposed by
// `nvcf-cli admin`. Useful for local end-to-end smoke testing without
// needing an NVCF backend or NVCF_TOKEN with admin scopes.
//
// Endpoints served:
//
//	GET    /v2/nvcf/accounts
//	PATCH  /v2/nvcf/accounts/{ncaId}
//	PUT    /v2/nvcf/accounts/{ncaId}/secrets/functions/{functionId}/versions/{versionId}
//	PUT    /v2/nvcf/accounts/{ncaId}/secrets/telemetries/{telemetryId}
//	GET    /v2/nvcf/accounts/{ncaId}/queues/functions/{functionId}
//	GET    /v2/nvcf/accounts/{ncaId}/queues/functions/{functionId}/versions/{versionId}
//
// Usage (from src/clis/nvcf-cli):
//
//	go run ./scripts/admin-mock [port]    # default port 9999
//
// Then in another shell:
//
//	export NVCF_BASE_HTTP_URL=http://localhost:9999
//	export NVCF_TOKEN=fake-admin-jwt
//	export NVCF_CLI_ENABLE_ADMIN=1
//	nvcf-cli admin accounts list
//	nvcf-cli admin accounts list --json
//
// Stop with Ctrl-C.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type account struct {
	NCAID                         string   `json:"ncaId"`
	Name                          string   `json:"name"`
	MaxFunctionsAllowed           int      `json:"maxFunctionsAllowed"`
	MaxTasksAllowed               int      `json:"maxTasksAllowed"`
	MaxTelemetriesAllowed         int      `json:"maxTelemetriesAllowed"`
	MaxRegistryCredentialsAllowed int      `json:"maxRegistryCredentialsAllowed"`
	AdminClientIDs                []string `json:"adminClientIds"`
}

var accounts = []account{
	{
		NCAID:                         "0123456789AB-CDEF",
		Name:                          "Acme Robotics",
		MaxFunctionsAllowed:           25,
		MaxTasksAllowed:               10,
		MaxTelemetriesAllowed:         5,
		MaxRegistryCredentialsAllowed: 3,
		AdminClientIDs:                []string{"acme-admin-1", "acme-admin-2"},
	},
	{
		NCAID:                         "9988776655AA-BBCC",
		Name:                          "Globex Industries",
		MaxFunctionsAllowed:           50,
		MaxTasksAllowed:               25,
		MaxTelemetriesAllowed:         10,
		MaxRegistryCredentialsAllowed: 5,
		AdminClientIDs:                []string{"globex-admin"},
	},
	{
		NCAID:                         "AABBCCDDEEFF-1122",
		Name:                          "Initech",
		MaxFunctionsAllowed:           5,
		MaxTasksAllowed:               2,
		MaxTelemetriesAllowed:         1,
		MaxRegistryCredentialsAllowed: 1,
		AdminClientIDs:                []string{},
	},
}

var (
	patchAccountRe        = regexp.MustCompile(`^/v2/nvcf/accounts/([^/]+)$`)
	putSecretsFunctionRe  = regexp.MustCompile(`^/v2/nvcf/accounts/[^/]+/secrets/functions/[^/]+/versions/[^/]+$`)
	putSecretsTelemetryRe = regexp.MustCompile(`^/v2/nvcf/accounts/[^/]+/secrets/telemetries/[^/]+$`)
	getQueuesVersionRe    = regexp.MustCompile(`^/v2/nvcf/accounts/([^/]+)/queues/functions/([^/]+)/versions/([^/]+)$`)
	getQueuesFunctionRe   = regexp.MustCompile(`^/v2/nvcf/accounts/([^/]+)/queues/functions/([^/]+)$`)
)

func okJSON(w http.ResponseWriter, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func noContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
}

// drainRequestBody reads and discards the request body. Required before
// sending the response on any body-bearing method so the client doesn't
// see a connection reset and the next request on the same connection
// isn't corrupted by leftover bytes.
func drainRequestBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(os.Stderr, "[mock] %s %s\n", r.Method, r.URL.Path)
	path := strings.SplitN(r.URL.Path, "?", 2)[0]
	switch r.Method {
	case http.MethodGet:
		handleGet(w, path)
	case http.MethodPatch:
		handlePatch(w, r, path)
	case http.MethodPut:
		handlePut(w, r, path)
	default:
		notFound(w)
	}
}

func handleGet(w http.ResponseWriter, path string) {
	if path == "/v2/nvcf/accounts" {
		okJSON(w, map[string]any{"cloudAccounts": accounts})
		return
	}
	if m := getQueuesVersionRe.FindStringSubmatch(path); m != nil {
		fn, ver := m[2], m[3]
		okJSON(w, map[string]any{
			"functionId": fn,
			"queues": []map[string]any{{
				"functionVersionId": ver,
				"functionName":      "demo-llm-v1",
				"functionStatus":    "ACTIVE",
				"queueDepth":        42,
			}},
		})
		return
	}
	if m := getQueuesFunctionRe.FindStringSubmatch(path); m != nil {
		fn := m[2]
		okJSON(w, map[string]any{
			"functionId": fn,
			"queues": []map[string]any{
				{
					"functionVersionId": "ver-001-prod",
					"functionName":      "demo-llm-v1",
					"functionStatus":    "ACTIVE",
					"queueDepth":        42,
				},
				{
					"functionVersionId": "ver-002-staging",
					"functionName":      "demo-llm-v1",
					"functionStatus":    "INACTIVE",
					"queueDepth":        0,
				},
			},
		})
		return
	}
	notFound(w)
}

func handlePatch(w http.ResponseWriter, r *http.Request, path string) {
	if m := patchAccountRe.FindStringSubmatch(path); m != nil {
		drainRequestBody(r)
		okJSON(w, map[string]any{"account": account{
			NCAID:                         m[1],
			Name:                          "Mock Updated Account",
			MaxFunctionsAllowed:           100,
			MaxTasksAllowed:               50,
			MaxTelemetriesAllowed:         10,
			MaxRegistryCredentialsAllowed: 5,
			AdminClientIDs:                []string{"mock-admin"},
		}})
		return
	}
	notFound(w)
}

func handlePut(w http.ResponseWriter, r *http.Request, path string) {
	if putSecretsFunctionRe.MatchString(path) {
		drainRequestBody(r)
		noContent(w)
		return
	}
	if putSecretsTelemetryRe.MatchString(path) {
		drainRequestBody(r)
		noContent(w)
		return
	}
	notFound(w)
}

func main() {
	port := 9999
	if len(os.Args) > 1 {
		p, err := strconv.Atoi(os.Args[1])
		if err != nil || p < 1 || p > 65535 {
			fmt.Fprintf(os.Stderr, "invalid port %q: must be 1-65535\n", os.Args[1])
			os.Exit(2)
		}
		port = p
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(handler),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "\nshutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "nvcf admin mock listening on http://%s\n", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
