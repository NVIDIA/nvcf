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

package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestInvokeConfigParsesVanityGatewayFromJSON(t *testing.T) {
	t.Parallel()

	rawJSON := `{
		"vanityHost": "vanity.localhost",
		"path": "/bdd/echo",
		"requestBody": {"message": "test"}
	}`

	var config InvokeConfig
	if err := json.Unmarshal([]byte(rawJSON), &config); err != nil {
		t.Fatalf("unmarshal invoke config: %v", err)
	}

	if config.VanityHost != "vanity.localhost" {
		t.Fatalf("vanityHost = %q, want %q", config.VanityHost, "vanity.localhost")
	}
	if config.Path != "/bdd/echo" {
		t.Fatalf("path = %q, want %q", config.Path, "/bdd/echo")
	}
}

func TestValidateInvokeConfigVanityGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *InvokeConfig
		useGRPC     bool
		wantErrText string
	}{
		{
			name: "valid vanity gateway config with path",
			config: &InvokeConfig{
				VanityHost:  "vanity.localhost",
				Path:        "/bdd/echo",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			wantErrText: "",
		},
		{
			name: "vanity gateway with grpc fails",
			config: &InvokeConfig{
				VanityHost:  "vanity.localhost",
				Path:        "/bdd/echo",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			useGRPC:     true,
			wantErrText: "--vanity-host is not supported with --grpc",
		},
		{
			name: "valid vanity gateway config with inference-url fallback",
			config: &InvokeConfig{
				VanityHost:   "llama.api.myorg.com",
				InferenceURL: "/v1/chat/completions",
				RequestBody:  map[string]interface{}{"model": "llama-3"},
			},
			wantErrText: "",
		},
		{
			name: "vanity gateway without path or inference-url fails",
			config: &InvokeConfig{
				VanityHost:  "vanity.localhost",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			wantErrText: "path (or --inference-url) is required when using --vanity-host",
		},
		{
			name: "vanity gateway without request body fails",
			config: &InvokeConfig{
				VanityHost: "vanity.localhost",
				Path:       "/bdd/echo",
			},
			wantErrText: "request body is required",
		},
		{
			name: "path without vanity-host fails",
			config: &InvokeConfig{
				FunctionID:  "func-123",
				VersionID:   "ver-123",
				Path:        "/bdd/echo",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			wantErrText: "--path is only supported with --vanity-host",
		},
		{
			name: "standard invoke requires function ID",
			config: &InvokeConfig{
				VersionID:   "ver-123",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			wantErrText: "function ID is required",
		},
		{
			name: "standard invoke requires version ID",
			config: &InvokeConfig{
				FunctionID:  "func-123",
				RequestBody: map[string]interface{}{"message": "test"},
			},
			wantErrText: "version ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInvokeConfig(tt.config, tt.useGRPC)
			if tt.wantErrText == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("validateInvokeConfig() error = %v, want containing %q", err, tt.wantErrText)
			}
		})
	}
}

func TestInvokeOptionsFromConfigVanityGateway(t *testing.T) {
	t.Parallel()

	config := &InvokeConfig{
		VanityHost:          "vanity.localhost",
		Path:                "/bdd/echo",
		PollDurationSeconds: 15,
	}

	opts := invokeOptionsFromConfig(config)
	if opts == nil {
		t.Fatal("expected invokeOptionsFromConfig to return non-nil options")
	}
	if opts.VanityHost != "vanity.localhost" {
		t.Fatalf("opts.VanityHost = %q, want %q", opts.VanityHost, "vanity.localhost")
	}
	if opts.Path != "/bdd/echo" {
		t.Fatalf("opts.Path = %q, want %q", opts.Path, "/bdd/echo")
	}
	// InferenceURL must stay scoped to config.InferenceURL: the client's
	// Vanity Gateway branch falls back from Path to InferenceURL on its own,
	// and letting --path leak into InferenceURL here would let a plain REST
	// invocation (no --vanity-host) silently route to --path instead of the
	// function's configured endpoint.
	if opts.InferenceURL != "" {
		t.Fatalf("opts.InferenceURL = %q, want empty (Path must not populate InferenceURL)", opts.InferenceURL)
	}
	if opts.PollDurationSeconds != 15 {
		t.Fatalf("opts.PollDurationSeconds = %d, want 15", opts.PollDurationSeconds)
	}
}

func TestInvokeOptionsFromConfigDoesNotLeakPathIntoInferenceURLForStandardInvoke(t *testing.T) {
	t.Parallel()

	// Regression test: --path must never override the function's inference
	// URL for a standard (non-Vanity-Gateway) invocation.
	config := &InvokeConfig{
		FunctionID:   "func-123",
		VersionID:    "ver-456",
		InferenceURL: "/v1/chat/completions",
	}

	opts := invokeOptionsFromConfig(config)
	if opts == nil {
		t.Fatal("expected invokeOptionsFromConfig to return non-nil options")
	}
	if opts.InferenceURL != "/v1/chat/completions" {
		t.Fatalf("opts.InferenceURL = %q, want %q", opts.InferenceURL, "/v1/chat/completions")
	}
}

func TestLoadInvokeConfigVanityGatewayFlags(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().StringVar(&invokeFlags.vanityHost, "vanity-host", "", "")
	cmd.Flags().StringVar(&invokeFlags.path, "path", "", "")
	cmd.Flags().StringVar(&invokeFlags.requestBody, "request-body", "", "")

	args := []string{"--vanity-host", "vanity.localhost", "--path", "/bdd/echo", "--request-body", `{"key":"val"}`}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	config, err := loadInvokeConfig(cmd)
	if err != nil {
		t.Fatalf("loadInvokeConfig failed: %v", err)
	}
	if config.VanityHost != "vanity.localhost" {
		t.Fatalf("config.VanityHost = %q, want %q", config.VanityHost, "vanity.localhost")
	}
	if config.Path != "/bdd/echo" {
		t.Fatalf("config.Path = %q, want %q", config.Path, "/bdd/echo")
	}
}
