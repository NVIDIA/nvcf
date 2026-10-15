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

package worker

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

// newEmbeddedNats starts an in-process NATS server on an OS-assigned ephemeral
// port (Port: -1) so multiple test package binaries can run in parallel without
// colliding on the fixed 4222/4282 ports used by testutils.NewNatsSuperCluster.
//
// It mirrors the parts of the upstream supercluster the worker code relies on:
// JetStream enabled and the three aws-region server tags
// (aws-region:region-1/2/3) that the consumer placement logic expects. The
// server is wrapped in a *testutils.SuperCluster so existing call sites that
// read sc.Clusters[0].Servers[0].ClientURL() continue to work unchanged. The
// returned SuperCluster.Shutdown is a no-op (its private singleServer is nil);
// the embedded server is torn down via t.Cleanup instead.
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

type natsError string

func (e natsError) Error() string { return string(e) }

const errNatsNotReady natsError = "embedded nats server not ready for connections"
