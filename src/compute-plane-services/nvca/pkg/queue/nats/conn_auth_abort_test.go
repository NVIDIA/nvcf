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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
)

// TestRepeatedAuthErrorsDoNotCloseTheConnection is the regression test for
// nats.IgnoreAuthErrorAbort, and it is the path that regresses most easily
// because nothing else in the suite exercises a rejected credential.
//
// Without that option, processAuthError sets nc.ar when the same auth error
// recurs on a server and doReconnect closes the connection, independently of
// MaxReconnects. Since the PSAT token handler returns "" on a failed fetch, the
// server reports an authorization violation rather than a network error, so a
// token-source blip overlapping a NATS blip would close the connection for
// good and only a pod restart would recover it. That restart is exactly what
// this PR removes from the liveness path, so the two changes have to hold
// together or the fix reintroduces the bug it is fixing.
//
// The server here accepts one nkey and the client presents a different one, so
// every attempt is rejected for the same reason, which is the condition that
// triggers the abort.
func TestRepeatedAuthErrorsDoNotCloseTheConnection(t *testing.T) {
	_, serverPubKey := newUserSeed(t)
	rejectedSeed, _ := newUserSeed(t)

	srv := runJetStreamServer(t, serverPubKey)
	t.Cleanup(srv.Shutdown)

	origURL := DefaultNATSURL
	DefaultNATSURL = srv.ClientURL()
	t.Cleanup(func() { DefaultNATSURL = origURL })

	obs := &recordingObserver{}
	qc, err := NewClient(context.Background(), "cluster",
		staticSecretsFetcher{secrets: auth.NATSSecrets{
			APIAuth: auth.NATSAPIAuthSecrets{UserSeed: string(rejectedSeed)},
		}}, obs)

	// RetryOnFailedConnect means construction succeeds even though the
	// credential is rejected; the client is left retrying.
	require.NoError(t, err, "an unusable credential must not fail construction outright")
	cl := qc.(*client)
	t.Cleanup(cl.nc.Close)

	// Give the client long enough to rotate through the pool several times and
	// hit the same auth error repeatedly, which is what triggers the abort.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		require.False(t, cl.ConnectionClosed(),
			"repeated auth errors must not close the connection for good")
		time.Sleep(50 * time.Millisecond)
	}

	assert.False(t, obs.seen(ConnStateClosed),
		"the observer must not see a closed connection, or the alert on state==3 fires on a recoverable condition")
	assert.NotEqual(t, ConnStateConnected, obs.first(),
		"a rejected credential must not be reported as connected")
}
