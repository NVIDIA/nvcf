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

package test_e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/router"

	"github.com/nats-io/nats-server/v2/test"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestService_NATSOutage_ReportsConnectionState walks one connection through
// an outage, a recovery, and a shutdown, and checks what operators can see.
func TestService_NATSOutage_ReportsConnectionState(t *testing.T) {
	setup := setupCommonTestInfrastructure(t)
	core, logs := observer.New(zapcore.InfoLevel)
	setup.Logger = zap.New(core)
	setup.startAuthServiceWithConfig(t, setup.createBaseConfig())
	r := router.New(setup.Logger, &router.Config{ServiceName: "test-service", NATS: setup.AuthService})

	probe := func(path string) int {
		w := httptest.NewRecorder()
		r.Engine().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code
	}
	expectProbes := func(stage string, healthz, readyz int) {
		t.Helper()
		if got := probe("/healthz"); got != healthz {
			t.Errorf("%s: /healthz = %d, want %d", stage, got, healthz)
		}
		if got := probe("/readyz"); got != readyz {
			t.Errorf("%s: /readyz = %d, want %d", stage, got, readyz)
		}
	}

	expectProbes("connected", http.StatusOK, http.StatusOK)
	expectStatusGauge(t, "connected", "connected")
	disconnectsBefore := connectionEventCount(t, "disconnect")
	reconnectsBefore := connectionEventCount(t, "reconnect")

	setup.NATSServer.Shutdown()
	waitFor(t, 5*time.Second, "readiness to fail after NATS shutdown", func() bool {
		return probe("/readyz") == http.StatusServiceUnavailable
	})
	expectProbes("NATS down", http.StatusOK, http.StatusServiceUnavailable)
	// The handler updates metrics before it logs, so wait for the log line first.
	expectLog(t, logs, "NATS connection lost", zapcore.WarnLevel)
	expectStatusGauge(t, "NATS down", "reconnecting")
	if got := connectionEventCount(t, "disconnect"); got <= disconnectsBefore {
		t.Errorf("disconnect events = %v, want more than %v", got, disconnectsBefore)
	}

	restarted, _ := test.RunServerWithConfig(setup.ConfigPath)
	t.Cleanup(restarted.Shutdown)
	waitFor(t, 10*time.Second, "readiness to recover after NATS restart", func() bool {
		return probe("/readyz") == http.StatusOK
	})
	expectProbes("NATS back", http.StatusOK, http.StatusOK)
	expectLog(t, logs, "NATS connection restored", zapcore.InfoLevel)
	expectStatusGauge(t, "NATS back", "connected")
	if got := connectionEventCount(t, "reconnect"); got <= reconnectsBefore {
		t.Errorf("reconnect events = %v, want more than %v", got, reconnectsBefore)
	}

	setup.AuthService.Stop()
	waitFor(t, 5*time.Second, "liveness to fail after the connection closes", func() bool {
		return probe("/healthz") == http.StatusServiceUnavailable
	})
	// A shutdown close is expected, so it must not be reported as an error.
	expectLog(t, logs, "NATS connection closed", zapcore.InfoLevel)
	expectStatusGauge(t, "stopped", "closed")
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// expectLog waits for the first log line with message and checks its level.
func expectLog(t *testing.T, logs *observer.ObservedLogs, message string, level zapcore.Level) {
	t.Helper()
	waitFor(t, 5*time.Second, fmt.Sprintf("the %q log line", message), func() bool {
		return logs.FilterMessage(message).Len() > 0
	})
	if got := logs.FilterMessage(message).All()[0].Level; got != level {
		t.Errorf("%q logged at %s, want %s", message, got, level)
	}
}

// connectionEventCount sums the lifecycle counter for one event over all reasons.
func connectionEventCount(t *testing.T, event string) float64 {
	t.Helper()
	var total float64
	for _, m := range gatherSeries(t, "nvcf_nats_auth_callout_nats_connection_events_total") {
		if m.labels["event"] == event {
			total += m.value
		}
	}
	return total
}

func expectStatusGauge(t *testing.T, stage, want string) {
	t.Helper()
	for _, m := range gatherSeries(t, "nvcf_nats_auth_callout_nats_connection_status") {
		status := m.labels["status"]
		if (status == want) != (m.value == 1) {
			t.Errorf("%s: connection status %q gauge = %v, want only %q set", stage, status, m.value, want)
		}
	}
}

type series struct {
	labels map[string]string
	value  float64
}

// gatherSeries reads every exported series of one metric, so new label values are covered without test changes.
func gatherSeries(t *testing.T, name string) []series {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var out []series
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			out = append(out, series{labels: labels, value: m.GetCounter().GetValue() + m.GetGauge().GetValue()})
		}
	}
	if len(out) == 0 {
		t.Fatalf("metric %s not exported", name)
	}
	return out
}
