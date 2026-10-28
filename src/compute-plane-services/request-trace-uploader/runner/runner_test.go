// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry/otlp"

	_ "github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/debug"
)

func lookupOf(env map[string]string) config.LookupFunc {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

func environOf(env map[string]string) []string {
	environ := make([]string, 0, len(env))
	for name, value := range env {
		environ = append(environ, name+"="+value)
	}
	return environ
}

func TestLoadConfigAppliesTheOptions(t *testing.T) {
	const secret = "value-marker-41c"
	debug, objectstore := string(config.BackendDebug), string(config.BackendObjectStore)
	tests := map[string]struct {
		env     map[string]string
		opts    []Option
		backend config.Backend
		rules   int
		// named is the setting the error must name; empty means no error.
		named string
	}{
		"default backend when none is set": {
			env: map[string]string{}, opts: []Option{WithDefaultBackend(config.BackendDebug)}, backend: config.BackendDebug,
		},
		"default backend when the setting is blank": {
			env: map[string]string{config.EnvBackend: " "}, opts: []Option{WithDefaultBackend(config.BackendDebug)}, backend: config.BackendDebug,
		},
		"a set backend wins over the default": {
			env: map[string]string{config.EnvBackend: debug}, opts: []Option{WithDefaultBackend(config.Backend(objectstore))}, backend: config.BackendDebug,
		},
		"no backend and no default": {
			env: map[string]string{}, named: config.EnvBackend,
		},
		"required setting absent": {
			env: map[string]string{config.EnvBackend: debug}, opts: []Option{WithRequiredSettings(config.EnvSuppressNCAIDs)}, named: config.EnvSuppressNCAIDs,
		},
		"required setting empty": {
			env: map[string]string{config.EnvBackend: debug, config.EnvSuppressNCAIDs: ""}, opts: []Option{WithRequiredSettings(config.EnvSuppressNCAIDs)}, backend: config.BackendDebug,
		},
		"required setting set": {
			env: map[string]string{config.EnvBackend: debug, config.EnvSuppressNCAIDs: "nca-acme-nca"}, opts: []Option{WithRequiredSettings(config.EnvSuppressNCAIDs)}, backend: config.BackendDebug, rules: 1,
		},
		"unknown setting": {
			env: map[string]string{config.EnvBackend: debug, "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL": secret}, named: "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL",
		},
		"invalid value": {
			env: map[string]string{config.EnvBackend: debug, config.EnvScanInterval: secret}, named: config.EnvScanInterval,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tt.env[config.EnvSourceDir] = "/spool"
			cfg, err := loadConfig(lookupOf(tt.env), environOf(tt.env), collect(tt.opts))
			if tt.named != "" {
				if err == nil || !strings.Contains(err.Error(), tt.named) || strings.Contains(err.Error(), secret) {
					t.Fatalf("loadConfig() error = %v, want one naming %s and no value", err, tt.named)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.Backend != tt.backend || len(cfg.Suppression.Rules) != tt.rules {
				t.Fatalf("backend = %q with %d suppression rules, want %q with %d", cfg.Backend, len(cfg.Suppression.Rules), tt.backend, tt.rules)
			}
		})
	}
}

// lockedBuffer is a bytes.Buffer safe for the service's goroutines to log
// into while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return buf
}

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

// validEnv is a configuration that starts with the debug backend, names no
// backend so a test can default it, and drains within a second.
func validEnv(t *testing.T) map[string]string {
	t.Helper()
	secrets := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		config.EnvSourceDir:    t.TempDir(),
		config.EnvHealthAddr:   freeAddr(t),
		config.EnvSecretsFile:  secrets,
		config.EnvDrainTimeout: "1",
	}
}

// serve runs run until the service is ready, then cancels it as a signal
// would and returns the exit code.
func serve(t *testing.T, env map[string]string, distribution string, opts ...Option) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := make(chan int, 1)
	go func() { exited <- run(ctx, lookupOf(env), environOf(env), distribution, collect(opts)) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+env[config.EnvHealthAddr]+"/readyz", nil)
		if response, err := http.DefaultClient.Do(request); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case code := <-exited:
			t.Fatalf("run exited %d before it was ready", code)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready after 10 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-exited:
		return code
	case <-time.After(15 * time.Second):
		t.Fatal("run still running 15 s after shutdown")
		return -1
	}
}

func TestRunServesUntilShutdownThenExitsZero(t *testing.T) {
	logs := captureLog(t)

	if code := serve(t, validEnv(t), "test-build", WithDefaultBackend(config.BackendDebug)); code != 0 {
		t.Fatalf("exit = %d after shutdown, want 0\n%s", code, logs.String())
	}
	out := logs.String()
	for _, want := range []string{`"msg":"request trace uploader starting"`, `"backend":"debug"`, `"distribution":"test-build"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s\n%s", want, out)
		}
	}
}

func TestRunExitsOneWithoutStartingOnABadConfiguration(t *testing.T) {
	tests := map[string]struct {
		env   map[string]string
		opts  []Option
		named string
	}{
		"unknown setting": {
			env:   map[string]string{"REQUEST_TRACE_UPLOADER_SCAN_INTERVAL": "30"},
			named: "REQUEST_TRACE_UPLOADER_SCAN_INTERVAL",
		},
		"required setting absent": {
			opts:  []Option{WithRequiredSettings(config.EnvSuppressNCAIDs)},
			named: config.EnvSuppressNCAIDs,
		},
		"metrics endpoint without protocol": {
			env:   map[string]string{otlp.EnvMetricsEndpoint: "http://127.0.0.1:4318/v1/metrics"},
			named: otlp.EnvMetricsProtocol,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			env := validEnv(t)
			for k, v := range tt.env {
				env[k] = v
			}
			exited := make(chan int, 1)
			go func() {
				exited <- run(context.Background(), lookupOf(env), environOf(env), "test-build", collect(append(tt.opts, WithDefaultBackend(config.BackendDebug))))
			}()
			select {
			case code := <-exited:
				if code != 1 {
					t.Fatalf("exit = %d, want 1\n%s", code, logs.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("run still running after 5 s, want it to stop at startup\n%s", logs.String())
			}
			out := logs.String()
			if !strings.Contains(out, "invalid request trace uploader configuration") || !strings.Contains(out, tt.named) {
				t.Fatalf("log = %s, want the configuration error naming %s", out, tt.named)
			}
			if strings.Contains(out, "request trace uploader starting") {
				t.Fatalf("log = %s, want no start", out)
			}
		})
	}
}

// collector keeps the backend label of each metric an OTLP/HTTP export
// carried.
type collector struct {
	mu       sync.Mutex
	backends map[string]string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var export colmetricpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	for _, rm := range export.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				for _, point := range m.GetSum().GetDataPoints() {
					for _, kv := range point.GetAttributes() {
						if kv.GetKey() == telemetry.BackendLabel {
							c.backends[m.GetName()] = kv.GetValue().GetStringValue()
						}
					}
				}
			}
		}
	}
	c.mu.Unlock()
	out, _ := proto.Marshal(&colmetricpb.ExportMetricsServiceResponse{})
	_, _ = w.Write(out)
}

func (c *collector) backend(name string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	backend, ok := c.backends[name]
	return backend, ok
}

// The export interval is a minute, so what the collector gets in this test
// is the final export after the drain.
func TestRunSendsTheLastMetricsAfterTheDrain(t *testing.T) {
	logs := captureLog(t)
	received := &collector{backends: map[string]string{}}
	server := httptest.NewServer(received)
	defer server.Close()
	env := validEnv(t)
	env[otlp.EnvMetricsEndpoint] = server.URL + "/v1/metrics"
	env[otlp.EnvMetricsProtocol] = "http/protobuf"

	if code := serve(t, env, "test-build", WithDefaultBackend(config.BackendDebug)); code != 0 {
		t.Fatalf("exit = %d after shutdown, want 0\n%s", code, logs.String())
	}
	name := otlp.MetricPrefix + telemetry.SubmitsAmbiguous
	if backend, ok := received.backend(name); !ok || backend != string(config.BackendDebug) {
		t.Fatalf("collector got %s with backend %q (%v), want it from the final export with backend debug\n%s", name, backend, ok, logs.String())
	}
}
