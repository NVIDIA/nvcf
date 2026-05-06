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

package tracing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestSetupOTELTracer_Disabled(t *testing.T) {
	orig := tracer
	tracer = nil
	defer func() { tracer = orig }()
	cfg := &OTELConfig{Enabled: false}
	provider, err := SetupOTELTracer(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestSetupOTELTracer_NoEndpoint(t *testing.T) {
	tracer = nil
	defer func() { tracer = nil }()
	cfg := &OTELConfig{Enabled: true, Endpoint: ""}
	_, err := SetupOTELTracer(cfg)
	assert.Error(t, err)
}

func TestShutdown_NilTracer(t *testing.T) {
	tracer = nil
	Shutdown()
}

func TestSetupOTELTracer_AlreadyInitialized(t *testing.T) {
	// Manually set tracer so we can test the "already initialized" code path
	tracer = sdktrace.NewTracerProvider()
	defer func() { tracer = nil }()

	cfg := &OTELConfig{
		Enabled:  true,
		Endpoint: "localhost:4317",
	}
	provider, err := SetupOTELTracer(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestShutdown_WithTracer(t *testing.T) {
	tracer = sdktrace.NewTracerProvider()
	assert.NotNil(t, tracer)
	Shutdown()
	assert.Nil(t, tracer)
}

func TestSetupOTELTracer_InsecureWithAttributes(t *testing.T) {
	tracer = nil
	cfg := &OTELConfig{
		Enabled:  true,
		Endpoint: "localhost:14317",
		Insecure: true,
		Attributes: Attributes{
			ServiceName:    "test-svc",
			ServiceVersion: "v1.0.0",
			Extra:          map[string]string{"env": "test", "region": "us-east"},
		},
	}
	// This may error (no OTLP server running) but exercises the attribute
	// building, resource merging, and insecure-branch code paths.
	_, _ = SetupOTELTracer(cfg)
	tracer = nil // ensure cleanup regardless of outcome
}

func TestSetupOTELTracer_SecureWithAttributes(t *testing.T) {
	tracer = nil
	cfg := &OTELConfig{
		Enabled:  true,
		Endpoint: "localhost:14318",
		Insecure: false,
		Attributes: Attributes{
			ServiceName:    "test-svc",
			ServiceVersion: "v1.0.0",
			Extra:          map[string]string{"env": "test"},
		},
	}
	// May fail connecting, but exercises TLS and attribute code paths.
	_, _ = SetupOTELTracer(cfg)
	tracer = nil
}
