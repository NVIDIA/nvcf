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
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"nvcf-grpc-proxy/proxy/internal/test"
)

// /livez must stay up while NATS is connected or reconnecting, and fail only
// once the connection is closed and a restart is the only recovery.
func TestLivenessFailsOnlyWhenNatsIsClosed(t *testing.T) {
	server, err := test.NewFakeNatsServer(test.NatsAcceptThenDrop)
	require.NoError(t, err)

	nc, err := nats.Connect(server.URL,
		nats.Token(test.FakeNatsToken),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(10*time.Millisecond),
		nats.ReconnectJitter(0, 0))
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	livez := httptest.NewServer(livenessHandler(nc))
	t.Cleanup(livez.Close)
	get := func() int {
		response, err := http.Get(livez.URL + "/livez")
		require.NoError(t, err)
		_ = response.Body.Close()
		return response.StatusCode
	}

	require.Equal(t, http.StatusOK, get())

	// The server drops the connection and stops listening, so the client keeps
	// reconnecting.
	require.NoError(t, server.Close())
	require.Eventually(t, func() bool { return nc.Status() == nats.RECONNECTING },
		5*time.Second, 10*time.Millisecond, "status %s", nc.Status())
	require.Equal(t, http.StatusOK, get())

	nc.Close()
	require.Equal(t, http.StatusServiceUnavailable, get())
}
