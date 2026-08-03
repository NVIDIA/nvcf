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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/loadgen"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/sink"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
)

func TestShapesFromFlag(t *testing.T) {
	tests := []struct {
		in      string
		want    []spec.Shape
		wantErr bool
	}{
		{"container", []spec.Shape{spec.ShapeContainer}, false},
		{"helm", []spec.Shape{spec.ShapeHelm}, false},
		{"both", []spec.Shape{spec.ShapeContainer, spec.ShapeHelm}, false},
		{"bogus", nil, true},
	}
	for _, tt := range tests {
		got, err := shapesFromFlag(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("shapesFromFlag(%q): expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("shapesFromFlag(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("shapesFromFlag(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRenderCmdDefaults(t *testing.T) {
	cmd := newRenderCmd()
	defaults := map[string]string{
		"shape":     "both",
		"profile":   "dev",
		"namespace": "byoo-perf",
		"output":    "summary",
	}
	for name, want := range defaults {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("render command missing --%s flag", name)
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
	if f := cmd.Flags().Lookup("collector-image"); f == nil || f.DefValue != spec.DefaultCollectorImage {
		t.Errorf("--collector-image default = %v, want %q", f, spec.DefaultCollectorImage)
	}
}

func TestRenderCmdInvalidSelectors(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "nope"},
		{"--shape", "nope"},
		{"--output", "nope"},
	} {
		cmd := newRenderCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("render %v: expected error, got nil", args)
		}
	}
}

func TestRenderCmdJSONIsSingleValidArray(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newRenderCmd()
	cmd.SetArgs([]string{"--shape", "both", "--output", "json"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}

	// stdout must be a single valid JSON document (an array of both pods).
	var pods []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &pods); err != nil {
		t.Fatalf("stdout is not valid JSON array: %v\n%s", err, stdout.String())
	}
	if len(pods) != 2 {
		t.Errorf("expected 2 pods in JSON array, got %d", len(pods))
	}
	// Diagnostics must not pollute stdout.
	if strings.Contains(stdout.String(), "profile=") {
		t.Errorf("stdout leaked diagnostics: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "profile=") {
		t.Errorf("expected profile diagnostics on stderr, got: %s", stderr.String())
	}
}

func TestRenderCmdYAMLIsMultiDocStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newRenderCmd()
	cmd.SetArgs([]string{"--shape", "both", "--output", "yaml"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "\n---\n") {
		t.Errorf("expected a document separator between shapes, got:\n%s", out)
	}
	if got := strings.Count(out, "kind: Pod"); got != 2 {
		t.Errorf("expected 2 Pod documents, got %d:\n%s", got, out)
	}
	if strings.Contains(out, "# shape=") {
		t.Errorf("stdout should not contain the diagnostic shape header: %s", out)
	}
}

func TestRenderCmdSummary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newRenderCmd()
	cmd.SetArgs([]string{"--shape", "container", "--output", "summary"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(stdout.String(), "VALID") {
		t.Errorf("expected summary to report VALID, got: %s", stdout.String())
	}
}

func TestRunCmdDefaults(t *testing.T) {
	cmd := newRunCmd()
	defaults := map[string]string{
		"shape":         "both",
		"profile":       "dev",
		"mode":          "k3d",
		"namespace":     "byoo-perf",
		"ready-timeout": "3m0s",
		"retain":        "false",
		"skip-load":     "false",
		"sink-image":    sink.DefaultImage,
		"loadgen-image": loadgen.DefaultImage,
	}
	for name, want := range defaults {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("run command missing --%s flag", name)
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
}

// TestRunCmdInvalidSelectors asserts run rejects bad selectors before it ever
// touches a cluster, so these stay hermetic (no kubeconfig required).
func TestRunCmdInvalidSelectors(t *testing.T) {
	for _, args := range [][]string{
		{"--mode", "nope"},
		{"--profile", "nope"},
		{"--shape", "nope"},
	} {
		cmd := newRunCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("run %v: expected error, got nil", args)
		}
	}
}

func TestCleanupCmdDefaults(t *testing.T) {
	cmd := newCleanupCmd()
	defaults := map[string]string{
		"shape":     "both",
		"namespace": "byoo-perf",
	}
	for name, want := range defaults {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("cleanup command missing --%s flag", name)
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
}

func TestCleanupCmdInvalidShape(t *testing.T) {
	cmd := newCleanupCmd()
	cmd.SetArgs([]string{"--shape", "nope"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("cleanup --shape nope: expected error, got nil")
	}
}

func TestNamespaceForShape(t *testing.T) {
	if got := namespaceForShape("byoo-perf", spec.ShapeContainer, false); got != "byoo-perf" {
		t.Errorf("single-shape namespace = %q, want %q", got, "byoo-perf")
	}
	if got := namespaceForShape("byoo-perf", spec.ShapeContainer, true); got != "byoo-perf-container" {
		t.Errorf("multi-shape namespace = %q, want %q", got, "byoo-perf-container")
	}
	if got := namespaceForShape("byoo-perf", spec.ShapeHelm, true); got != "byoo-perf-helm" {
		t.Errorf("multi-shape namespace = %q, want %q", got, "byoo-perf-helm")
	}
}
