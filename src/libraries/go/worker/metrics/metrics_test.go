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

package metrics

import (
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestRegisterSharedMetrics(t *testing.T) {
	// First call wires up the gauge through the promauto default registry.
	registerSharedMetrics(WorkerInitRootNamespace)
	require.NotNil(t, TokenRefreshFailureGauge)

	TokenRefreshFailureGauge.WithLabelValues("nca").Set(3)
	var m dto.Metric
	require.NoError(t, TokenRefreshFailureGauge.WithLabelValues("nca").Write(&m))
	require.Equal(t, 3.0, m.GetGauge().GetValue())

	// Second call is a no-op via sync.Once: must not panic on re-registration
	// and the gauge pointer is unchanged.
	before := TokenRefreshFailureGauge
	registerSharedMetrics(NvcfRootNamespace)
	require.Same(t, before, TokenRefreshFailureGauge)
}

func TestInitNatsStats(t *testing.T) {
	// Embedded NATS server on an OS-assigned port (Port: -1), no fixed binding.
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second), "embedded nats server did not start")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	// Registers the five GaugeFunc collectors against the default registry.
	// Guarded by sync.Once, so a second call is a no-op (no duplicate-register panic).
	require.NotPanics(t, func() {
		initNatsStats(nc)
		initNatsStats(nc)
	})

	// The GaugeFunc closures read live connection stats; gather to confirm they
	// evaluate without error.
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	found := false
	for _, mf := range mfs {
		if mf.GetName() == NatsNamespace+"_out_bytes" {
			found = true
		}
	}
	require.True(t, found, "expected nats out_bytes gauge to be registered")
}
