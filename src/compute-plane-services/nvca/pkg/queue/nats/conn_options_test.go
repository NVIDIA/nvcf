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

package nats

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
)

// TestResilienceOptionsValues pins the three settings that decide whether a
// NATS outage is survivable. Each has a specific failure mode if dropped, noted
// in the assertion message.
func TestResilienceOptionsValues(t *testing.T) {
	var o nats.Options
	for _, opt := range resilienceOptions() {
		require.NoError(t, opt(&o))
	}

	assert.Equal(t, -1, o.MaxReconnect,
		"a finite limit lets the server pool empty, which closes the connection for good")
	assert.True(t, o.IgnoreAuthErrorAbort,
		"a repeated auth error otherwise aborts the reconnect regardless of MaxReconnect")
	assert.True(t, o.RetryOnFailedConnect,
		"NATS being down at startup otherwise fails Agent.Start and crash-loops the pod")
}

// TestConstructorAppliesResilienceOptions is the guard for the two connect paths
// drifting apart. It asserts against a client built by the production
// constructor rather than by calling resilienceOptions directly, so removing the
// call from a connect path fails here.
func TestConstructorAppliesResilienceOptions(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)
	t.Cleanup(func() { _ = cl.nc.Drain() })

	assert.Equal(t, -1, cl.nc.Opts.MaxReconnect)
	assert.True(t, cl.nc.Opts.IgnoreAuthErrorAbort)
	assert.True(t, cl.nc.Opts.RetryOnFailedConnect)
}

// TestConnectionClosedTracksConnectionState covers the signal liveness now uses.
// It has to distinguish a live connection from one nats.go has given up on,
// because only the latter is unrecoverable without a restart.
func TestConnectionClosedTracksConnectionState(t *testing.T) {
	seed, pubKey := newUserSeed(t)
	srv := runJetStreamServer(t, pubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	fetcher := staticSecretsFetcher{secrets: auth.NATSSecrets{
		APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(seed)},
	}}

	qc, err := NewClient(context.Background(), "cluster", fetcher)
	require.NoError(t, err)
	cl := qc.(*client)

	assert.False(t, cl.ConnectionClosed(), "a working connection must not look closed")

	cl.nc.Close()
	assert.True(t, cl.ConnectionClosed(), "a closed connection must be reported as such")
}
