// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// metricNames collects the metric names an OTLP/HTTP receiver got.
type metricNames struct {
	mu    sync.Mutex
	names map[string]bool
}

func (m *metricNames) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var export colmetricpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	for _, rm := range export.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, metric := range sm.GetMetrics() {
				m.names[metric.GetName()] = true
			}
		}
	}
	m.mu.Unlock()
	out, _ := proto.Marshal(&colmetricpb.ExportMetricsServiceResponse{})
	_, _ = w.Write(out)
}

func (m *metricNames) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names[name]
}

// freeAddr returns a loopback address that was free a moment ago, so a test
// can poll the process's health endpoint.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// startMain runs main in a child process with a valid configuration plus
// extra, waits until it is ready, and returns it with its stderr.
func startMain(t *testing.T, extra ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	secrets := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	health := freeAddr(t)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append([]string{
		runMainEnv + "=1",
		config.EnvSourceDir + "=" + t.TempDir(),
		config.EnvBackend + "=" + string(config.BackendDebug),
		config.EnvHealthAddr + "=" + health,
		config.EnvSecretsFile + "=" + secrets,
		config.EnvDrainTimeout + "=1",
	}, extra...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+health+"/readyz", nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return cmd, &stderr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process not ready after 10 s\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stopMain sends SIGTERM and waits for the process to exit within limit.
func stopMain(t *testing.T, cmd *exec.Cmd, stderr *bytes.Buffer, limit time.Duration) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exit = %v, want a clean exit\n%s", err, stderr.String())
		}
	case <-time.After(limit):
		t.Fatalf("process still running %v after SIGTERM\n%s", limit, stderr.String())
	}
}

func TestMainExportsMetricsWhenTheEndpointIsSet(t *testing.T) {
	received := &metricNames{names: map[string]bool{}}
	collector := httptest.NewServer(received)
	defer collector.Close()

	cmd, stderr := startMain(t,
		telemetry.EnvOTLPMetricsEndpoint+"="+collector.URL+"/v1/metrics",
		telemetry.EnvOTLPMetricsProtocol+"=http/protobuf",
	)
	stopMain(t, cmd, stderr, 15*time.Second)

	if !received.has(telemetry.MetricPrefix + telemetry.SubmitsAmbiguous) {
		t.Fatalf("the collector got no %s%s on shutdown\n%s", telemetry.MetricPrefix, telemetry.SubmitsAmbiguous, stderr.String())
	}
}

func TestMainStaysReadyAndStopsWithADeadCollector(t *testing.T) {
	dead := "http://" + freeAddr(t) + "/v1/metrics"
	cmd, stderr := startMain(t,
		telemetry.EnvOTLPMetricsEndpoint+"="+dead,
		telemetry.EnvOTLPMetricsProtocol+"=http/protobuf",
	)
	// Drain is 1 s and the metrics flush is bounded, so the exit is prompt
	// even though no export can succeed.
	stopMain(t, cmd, stderr, 15*time.Second)
	if strings.Contains(stderr.String(), "invalid request trace uploader configuration") {
		t.Fatalf("a dead collector was treated as a configuration error\n%s", stderr.String())
	}
}

func TestMainLogsNoCollectorDetailsWhenExportFails(t *testing.T) {
	const pathMarker, bodyMarker = "/v1/metrics-path-marker", "body-marker-7f3a"
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, bodyMarker, http.StatusBadRequest)
	}))
	defer collector.Close()

	cmd, stderr := startMain(t,
		telemetry.EnvOTLPMetricsEndpoint+"="+collector.URL+pathMarker,
		telemetry.EnvOTLPMetricsProtocol+"=http/protobuf",
	)
	stopMain(t, cmd, stderr, 15*time.Second)

	out := stderr.String()
	if !strings.Contains(out, "final metrics export failed") {
		t.Fatalf("stderr = %s, want the failed final export reported", out)
	}
	for _, leak := range []string{strings.TrimPrefix(collector.URL, "http://"), pathMarker, bodyMarker, "Bad Request"} {
		if strings.Contains(out, leak) {
			t.Errorf("stderr contains %q\n%s", leak, out)
		}
	}
}
