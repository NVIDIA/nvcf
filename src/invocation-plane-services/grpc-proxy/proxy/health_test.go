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

package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
)

func TestHealthFailsWhenNatsIsDisconnected(t *testing.T) {
	manager, err := healthManager(&nats.Conn{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	manager.HandlerFunc(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "nats") {
		t.Fatalf("body %q does not report the nats check", body)
	}
	if strings.Contains(body, "nvcf") {
		t.Fatalf("body %q reports an nvcf-api check; readiness must not depend on nvcf-api", body)
	}
}
