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

package ratelimitsync

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/llm-api-gateway/config"
)

func TestRunWorkerRequiresTransport(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Olric.Enabled = true

	err := RunWorker(context.Background(), cfg)
	if err == nil {
		t.Fatal("RunWorker() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "RATE_LIMIT_SYNC_TRANSPORT") {
		t.Fatalf("error %q must mention the offending env var", err.Error())
	}
}

func TestRunWorkerRequiresOlric(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.RateLimitSync.Transport = "pubsub"

	err := RunWorker(context.Background(), cfg)
	if err == nil {
		t.Fatal("RunWorker() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OLRIC_ENABLED") {
		t.Fatalf("error %q must mention OLRIC_ENABLED", err.Error())
	}
}

// TestWorkerRunnerForDispatchesByTransport is the narrow behaviour contract
// that protects RunWorker from a refactor accidentally routing one transport
// through the other. The returned funcs are compared by function identity so
// we can assert dispatch without invoking real I/O.
func TestWorkerRunnerForDispatchesByTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport string
		want      workerRunner
		wantErr   bool
	}{
		{name: "nats", transport: "nats", want: runNATSWorker},
		{name: "pubsub", transport: "pubsub", want: runPubSubWorker},
		{name: "NATS (case insensitive)", transport: "NATS", want: runNATSWorker},
		{name: "empty rejected", transport: "", wantErr: true},
		{name: "unknown rejected", transport: "kafka", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workerRunnerFor(tc.transport)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("workerRunnerFor(%q) error = nil, want error", tc.transport)
				}
				return
			}
			if err != nil {
				t.Fatalf("workerRunnerFor(%q) error = %v, want nil", tc.transport, err)
			}
			if ptrOf(got) != ptrOf(tc.want) {
				t.Fatalf("workerRunnerFor(%q) returned the wrong runner", tc.transport)
			}
		})
	}
}

// TestNewPublisherRuntimeNoOpForEmptyTransport pins the documented
// behaviour: no transport => a runtime that wires a no-op Synchronizer and
// whose Start/Stop cannot fail.
func TestNewPublisherRuntimeNoOpForEmptyTransport(t *testing.T) {
	t.Parallel()

	for _, transport := range []string{"", "none", "NONE"} {
		transport := transport
		t.Run(transport, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			cfg.RateLimitSync.Transport = transport

			runtime, err := NewPublisherRuntime(cfg)
			if err != nil {
				t.Fatalf("NewPublisherRuntime(%q) error = %v", transport, err)
			}
			if runtime == nil {
				t.Fatalf("NewPublisherRuntime(%q) returned nil runtime", transport)
			}
			if runtime.Synchronizer == nil {
				t.Fatalf("NewPublisherRuntime(%q) returned nil synchronizer", transport)
			}
			if err := runtime.Start(); err != nil {
				t.Fatalf("Start() error = %v, want nil", err)
			}
			runtime.Stop()
			runtime.Stop()
		})
	}
}

// TestNewPublisherRuntimeRejectsUnknownTransport pins the fail-loud branch of
// the transport switch: a typo in RATE_LIMIT_SYNC_TRANSPORT should stop
// startup, not silently fall through to the no-op runtime.
func TestNewPublisherRuntimeRejectsUnknownTransport(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.RateLimitSync.Transport = "kafka"

	_, err := NewPublisherRuntime(cfg)
	if err == nil {
		t.Fatal("NewPublisherRuntime() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "kafka") {
		t.Fatalf("error %q must echo the offending transport value", err.Error())
	}
}

// TestPublisherRuntimeNilSafe exercises the zero-value / nil-receiver paths
// so a caller that holds a PublisherRuntime by pointer can treat Start/Stop
// as always-safe, no matter whether construction succeeded.
func TestPublisherRuntimeNilSafe(t *testing.T) {
	t.Parallel()

	var runtime *PublisherRuntime
	if err := runtime.Start(); err != nil {
		t.Fatalf("nil Start() error = %v, want nil", err)
	}
	runtime.Stop()

	empty := &PublisherRuntime{}
	if err := empty.Start(); err != nil {
		t.Fatalf("empty Start() error = %v, want nil", err)
	}
	empty.Stop()
}

// ptrOf returns the code-address of a workerRunner so tests can compare
// runner identity. Go func values are not `==`-comparable, but
// reflect.Value.Pointer is the standard, vet-clean way to ask "is this the
// same function value?" for free functions (no closures).
func ptrOf(r workerRunner) uintptr {
	if r == nil {
		return 0
	}
	return reflect.ValueOf(r).Pointer()
}

func TestNewStopIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls int
	stop := newStop(func() {
		calls++
	})

	stop()
	stop()

	if calls != 1 {
		t.Fatalf("stop calls = %d, want 1", calls)
	}
}

func TestValidatedPubSubPublisherConfigDoesNotRequireSubscription(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.RateLimitSync.ClusterName = "cluster-a"
	cfg.RateLimitSync.PubSub.ProjectID = "project-a"
	cfg.RateLimitSync.PubSub.Topic = "topic-a"
	cfg.RateLimitSync.PubSub.Subscription = ""

	pubsubCfg, err := validatedPubSubPublisherConfig(cfg)
	if err != nil {
		t.Fatalf("validatedPubSubPublisherConfig() error = %v", err)
	}
	if pubsubCfg.Subscription != "" {
		t.Fatalf("publisher subscription = %q, want empty", pubsubCfg.Subscription)
	}
}

func TestValidatedPubSubWorkerConfigRequiresSubscription(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.RateLimitSync.ClusterName = "cluster-a"
	cfg.RateLimitSync.PubSub.ProjectID = "project-a"
	cfg.RateLimitSync.PubSub.Topic = "topic-a"
	cfg.RateLimitSync.PubSub.Subscription = ""

	if _, err := validatedPubSubWorkerConfig(cfg); err == nil {
		t.Fatal("validatedPubSubWorkerConfig() error = nil, want error")
	}
}
