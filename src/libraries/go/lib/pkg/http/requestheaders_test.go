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

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientRequestHeadersTransport(t *testing.T) {
	tests := []struct {
		name        string
		base        http.RoundTripper
		reqHeaders  map[string]string
		expectedRT  http.RoundTripper
		expectedHdr map[string]string
	}{
		{
			name:        "default transport",
			base:        nil,
			reqHeaders:  map[string]string{"Foo": "Bar"},
			expectedRT:  http.DefaultTransport,
			expectedHdr: map[string]string{"Foo": "Bar"},
		},
		{
			name:        "custom transport",
			base:        &http.Transport{},
			reqHeaders:  map[string]string{"Foo": "Bar"},
			expectedRT:  &http.Transport{},
			expectedHdr: map[string]string{"Foo": "Bar"},
		},
		{
			name:        "no headers",
			base:        http.DefaultTransport,
			reqHeaders:  nil,
			expectedRT:  http.DefaultTransport,
			expectedHdr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newClientRequestHeadersTransport(tt.base, tt.reqHeaders)
			assert.Equal(t, tt.expectedRT, tr.rt)
			assert.Equal(t, tt.expectedHdr, tr.headers)
		})
	}
}

func TestClientRequestHeadersTransportRoundTrip(t *testing.T) {
	tests := []struct {
		name        string
		reqHeaders  map[string]string
		headers     map[string]string
		expectedHdr map[string][]string
	}{
		{
			name:       "add header",
			reqHeaders: map[string]string{},
			headers: map[string]string{
				"Foo": "Bar",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
			},
		},
		{
			name: "overwrite header",
			reqHeaders: map[string]string{
				"Foo": "ClientValue",
			},
			headers: map[string]string{
				"Foo": "Bar",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
			},
		},
		{
			name:       "multiple headers",
			reqHeaders: map[string]string{},
			headers: map[string]string{
				"Foo": "Bar",
				"Baz": "Qux",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
				"Baz": {"Qux"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Subset(t, r.Header, tt.expectedHdr)
			}))
			t.Cleanup(s.Close)

			var options []Option
			for k, v := range tt.headers {
				options = append(options, WithRequestHeader(k, v))
			}
			c := NewRetryableClient(context.Background(), options...)
			req, err := http.NewRequest(http.MethodGet, s.URL, nil)
			for k, v := range tt.reqHeaders {
				req.Header.Set(k, v)
			}
			require.NoError(t, err)
			resp, err := c.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				if resp != nil {
					resp.Body.Close()
				}
			})
		})
	}
}
