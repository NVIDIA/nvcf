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

package tracing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// rtFunc adapts a func to http.RoundTripper (no CloseIdleConnections).
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// idlerRT implements both RoundTripper and closeIdler.
type idlerRT struct {
	rtFunc
	closed *bool
}

func (i idlerRT) CloseIdleConnections() { *i.closed = true }

func TestNewOtelTransportRoundTrips(t *testing.T) {
	called := false
	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	})

	rt := NewOtelTransport(base)
	require.NotNil(t, rt)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, called)
}

func TestNewOtelTransportCloseIdleConnectionsDelegates(t *testing.T) {
	closed := false
	base := idlerRT{
		rtFunc: rtFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
		}),
		closed: &closed,
	}

	rt := NewOtelTransport(base)
	idler, ok := rt.(interface{ CloseIdleConnections() })
	require.True(t, ok, "wrapper must expose CloseIdleConnections")
	idler.CloseIdleConnections()
	require.True(t, closed, "call must delegate to the underlying idler")
}

func TestNewOtelTransportCloseIdleConnectionsNoIdler(t *testing.T) {
	// Base does not implement closeIdler; CloseIdleConnections must be a safe no-op.
	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	})
	rt := NewOtelTransport(base)
	idler, ok := rt.(interface{ CloseIdleConnections() })
	require.True(t, ok)
	require.NotPanics(t, idler.CloseIdleConnections)
}
