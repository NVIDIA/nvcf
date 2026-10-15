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

package service

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

type natsError string

func (e natsError) Error() string { return string(e) }

const errNatsNotReady natsError = "embedded nats server not ready for connections"

// newEmbeddedNats starts an in-process NATS server on an OS-assigned ephemeral
// port (Port: -1) so this package's end-to-end suite does not collide with the
// worker / polling packages on the fixed 4222/4282 ports that
// testutils.NewNatsSuperCluster hardcodes, which would otherwise hang under
// `go test ./...`.
//
// It enables JetStream and sets the three aws-region server tags the consumer
// placement logic expects, and wraps the server in a *testutils.SuperCluster so
// existing call sites (sc.Clusters[0].Servers[0].ClientURL(), defer
// sc.Shutdown()) keep working. SuperCluster.Shutdown is a no-op here; the
// embedded server is torn down via t.Cleanup.
func newEmbeddedNats(t *testing.T) (*testutils.SuperCluster, error) {
	t.Helper()

	opts := &server.Options{
		Host:                   "127.0.0.1",
		Port:                   -1,
		JetStream:              true,
		StoreDir:               t.TempDir(),
		DisableJetStreamBanner: true,
		Tags:                   []string{"aws-region:region-1", "aws-region:region-2", "aws-region:region-3"},
		NoSigs:                 true,
	}

	s, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	s.Start()
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})

	if !s.ReadyForConnections(10 * time.Second) {
		return nil, errNatsNotReady
	}

	sc := &testutils.SuperCluster{
		Clusters: []*testutils.Cluster{
			{Region: "region-1", Servers: []*server.Server{s}},
			{Region: "region-2", Servers: []*server.Server{s}},
			{Region: "region-3", Servers: []*server.Server{s}},
		},
	}
	return sc, nil
}
