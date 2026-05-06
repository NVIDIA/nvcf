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

package fnds

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

func TestHTTPClient(t *testing.T) {
	mux := http.NewServeMux()
	wantErr := new(error)
	wantCode := new(int)
	mux.HandleFunc("/foo", func(w http.ResponseWriter, r *http.Request) {
		if *wantErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte((*wantErr).Error()))
			return
		}
		if *wantCode != 0 {
			w.WriteHeader(*wantCode)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Test rate limiting
	httpClient := newHTTPClient(1*time.Second, 5, 2)

	// Create some good requests but expect rate limiting.
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.Error(ct, err) {
			assert.Contains(ct, err.Error(), "rate limited")
			assert.Nil(ct, resp)
		}
	}, 2*time.Second, 10*time.Millisecond)

	// Test circuit breaking.
	httpClient = newHTTPClient(1*time.Second, rate.Limit(100), 100)

	for range 5 {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(t, err) {
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		}
	}

	// Break circuit on consecutive errors.
	*wantErr = fmt.Errorf("some error")
	for range 6 {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(t, err) {
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		}
	}
	resp, err := httpClient.Get(srv.URL + "/foo")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "circuit breaker is open")
		assert.Nil(t, resp)
	}

	// Wait for circuit to be half-closed.
	*wantErr = nil
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := httpClient.Get(srv.URL + "/foo")
		if assert.NoError(ct, err) {
			assert.Equal(ct, http.StatusOK, resp.StatusCode)
		}
	}, 2*time.Second, 100*time.Millisecond)

	// Break circuit on unexpected code.
	*wantCode = http.StatusNoContent
	resp, err = httpClient.Get(srv.URL + "/foo")
	if assert.NoError(t, err) {
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
	resp, err = httpClient.Get(srv.URL + "/foo")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "circuit breaker is open")
		assert.Nil(t, resp)
	}
}
