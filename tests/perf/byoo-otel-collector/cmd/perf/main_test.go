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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/deploy"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/k3d"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/loadgen"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/profile"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/report"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/sink"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
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
		"shape":          "both",
		"profile":        "dev",
		"mode":           "k3d",
		"namespace":      "byoo-perf",
		"ready-timeout":  "3m0s",
		"startup-target": "15s",
		"startup-max":    "30s",
		"retain":         "false",
		"skip-load":      "false",
		"sink-image":     sink.DefaultImage,
		"loadgen-image":  loadgen.DefaultImage,
		"k3d-cluster":    "byoo-perf",
		"import-images":  "false",
		"results-dir":    "",
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
		{"--startup-target", "0s"},
		{"--startup-target", "31s", "--startup-max", "30s"},
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

// TestRunCleansUpPodWhenServiceCreateFails verifies that when Deploy fails
// after the pod is created (here, because service creation is rejected), run
// rolls back the orphaned pod instead of leaking it, since --retain is false.
func TestRunCleansUpPodWhenServiceCreateFails(t *testing.T) {
	fakeCS := fake.NewSimpleClientset()
	fakeCS.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("service creation rejected")
	})

	orig := newDeployClient
	newDeployClient = func(string, string) (*deploy.Client, error) {
		return deploy.NewClientForClientset(fakeCS), nil
	}
	t.Cleanup(func() { newDeployClient = orig })

	cfg := runConfig{
		shape:          "container",
		profile:        "dev",
		mode:           "remote",
		collectorImage: spec.DefaultCollectorImage,
		namespace:      "byoo-perf",
		readyTimeout:   time.Second,
		startupTarget:  15 * time.Second,
		startupMax:     30 * time.Second,
	}
	if err := runRun(io.Discard, cfg); err == nil {
		t.Fatal("expected run to fail when service creation is rejected")
	}

	pods, err := fakeCS.CoreV1().Pods("byoo-perf").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("expected the pod to be cleaned up after a failed deploy, got %d", len(pods.Items))
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

// TestEnsureK3dClusterDoesNotDeleteReusedCluster verifies the suite never tears
// down a k3d cluster that already existed before the run.
func TestEnsureK3dClusterDoesNotDeleteReusedCluster(t *testing.T) {
	var calls [][]string
	orig := k3d.Runner
	k3d.Runner = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		// Report that the target cluster already exists so Create reuses it.
		if len(args) >= 2 && args[0] == "cluster" && args[1] == "list" {
			return []byte("byoo-perf 1/1\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { k3d.Runner = orig })

	cfg := runConfig{mode: "k3d", k3dCluster: "byoo-perf", retain: false}
	cluster, teardown, err := ensureK3dCluster(context.Background(), io.Discard, cfg)
	if err != nil {
		t.Fatalf("ensureK3dCluster: %v", err)
	}
	if !cluster.Reused {
		t.Fatal("expected the pre-existing cluster to be reported as reused")
	}

	teardown()
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "cluster" && c[1] == "delete" {
			t.Errorf("teardown deleted a reused cluster: calls=%v", calls)
		}
	}
}

// TestRunRepetitionsHonorsProfileCount asserts a multi-repetition profile runs
// its full load+measure cycle once per repetition, tagging each result with its
// run index instead of collapsing to a single sample.
func TestRunRepetitionsHonorsProfileCount(t *testing.T) {
	prof := profile.Profile{Name: "baseline", Repetitions: 3}
	var started, measured, waited int

	reports, err := runRepetitions(io.Discard, prof, spec.ShapeContainer,
		func(int) error { started++; return nil },
		func(int) report.ShapeReport { measured++; return report.ShapeReport{Status: report.StatusOK} },
		func(int) error { waited++; return nil },
	)
	if err != nil {
		t.Fatalf("runRepetitions: %v", err)
	}
	if started != 3 || measured != 3 || waited != 3 {
		t.Errorf("cycle counts = start:%d measure:%d wait:%d, want 3 each", started, measured, waited)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	for i, r := range reports {
		if r.Run != i+1 || r.Repetitions != 3 {
			t.Errorf("report %d tagged run=%d reps=%d, want run=%d reps=3", i, r.Run, r.Repetitions, i+1)
		}
	}
}

// A zero/omitted repetition count still yields exactly one run.
func TestRunRepetitionsDefaultsToOne(t *testing.T) {
	var measured int
	reports, err := runRepetitions(io.Discard, profile.Profile{}, spec.ShapeHelm,
		func(int) error { return nil },
		func(int) report.ShapeReport { measured++; return report.ShapeReport{} },
		func(int) error { return nil },
	)
	if err != nil {
		t.Fatalf("runRepetitions: %v", err)
	}
	if measured != 1 || len(reports) != 1 {
		t.Errorf("measured=%d reports=%d, want 1 each", measured, len(reports))
	}
}

// A waitLoad failure marks only that run invalid (with a reason) but does not
// abort the remaining repetitions.
func TestRunRepetitionsMarksInvalidOnWaitFailure(t *testing.T) {
	prof := profile.Profile{Name: "baseline", Repetitions: 2}
	reports, err := runRepetitions(io.Discard, prof, spec.ShapeContainer,
		func(int) error { return nil },
		func(int) report.ShapeReport { return report.ShapeReport{Status: report.StatusOK} },
		func(run int) error {
			if run == 1 {
				return fmt.Errorf("generator crashed")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runRepetitions: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	if reports[0].Status != report.StatusInvalid || reports[0].FailureReason == "" {
		t.Errorf("run 1 = status %q reason %q, want invalid with a reason", reports[0].Status, reports[0].FailureReason)
	}
	if reports[1].Status != report.StatusOK {
		t.Errorf("run 2 status = %q, want %q", reports[1].Status, report.StatusOK)
	}
}

// A startLoad failure aborts and surfaces the error so the caller can clean up.
func TestRunRepetitionsAbortsOnStartFailure(t *testing.T) {
	prof := profile.Profile{Name: "baseline", Repetitions: 3}
	var measured int
	_, err := runRepetitions(io.Discard, prof, spec.ShapeContainer,
		func(int) error { return fmt.Errorf("could not start load") },
		func(int) report.ShapeReport { measured++; return report.ShapeReport{} },
		func(int) error { return nil },
	)
	if err == nil {
		t.Fatal("expected startLoad failure to be returned")
	}
	if measured != 0 {
		t.Errorf("measure ran %d times after start failure, want 0", measured)
	}
}

// The generators must keep running until after the measurement window closes.
// loadGenDuration therefore exceeds warmup+window by a startup margin, so the
// startup delay before warmup cannot leave a load-free tail in the window.
func TestLoadGenDurationExceedsWindowByMargin(t *testing.T) {
	prof := profile.Profile{Warmup: 20 * time.Second, MeasurementWindow: 60 * time.Second}
	base := prof.Warmup + prof.MeasurementWindow
	got := loadGenDuration(prof)
	if got <= base {
		t.Fatalf("loadGenDuration = %s, want > warmup+window (%s) so the window stays under load", got, base)
	}
	if got != base+loadStartupMargin {
		t.Errorf("loadGenDuration = %s, want %s (warmup+window+margin)", got, base+loadStartupMargin)
	}
}

func TestStartupThresholds(t *testing.T) {
	if err := validateStartupThresholds(15*time.Second, 30*time.Second); err != nil {
		t.Fatalf("valid thresholds: %v", err)
	}
	for _, tt := range []struct {
		target time.Duration
		max    time.Duration
	}{
		{target: 0, max: 30 * time.Second},
		{target: 31 * time.Second, max: 30 * time.Second},
	} {
		if err := validateStartupThresholds(tt.target, tt.max); err == nil {
			t.Errorf("validateStartupThresholds(%s, %s): expected error", tt.target, tt.max)
		}
	}
}

func TestStartupHealthThresholdOutput(t *testing.T) {
	startup := report.NewStartupHealth(
		time.Unix(0, 0),
		time.Unix(2, 0),
		time.Unix(22, 0),
	)
	var out bytes.Buffer
	printStartupHealth(&out, spec.ShapeContainer, startup, 15*time.Second, 30*time.Second)
	if !strings.Contains(out.String(), "collector_to_health=20s") || !strings.Contains(out.String(), "exceeded the 15s target") {
		t.Errorf("startup output missing duration or warning:\n%s", out.String())
	}
	if err := checkStartupHealth(startup, 30*time.Second); err != nil {
		t.Fatalf("20s startup should meet 30s maximum: %v", err)
	}
	if err := checkStartupHealth(startup, 15*time.Second); err == nil {
		t.Fatal("20s startup should exceed 15s maximum")
	}
}

func TestPrintStartupHealthUsesUnroundedThresholds(t *testing.T) {
	for _, tt := range []struct {
		name        string
		collectorTo time.Duration
		wantWarning bool
	}{
		{name: "over target", collectorTo: 15*time.Second + 400*time.Microsecond, wantWarning: true},
		{name: "over maximum", collectorTo: 30*time.Second + 400*time.Microsecond, wantWarning: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startup := report.NewStartupHealth(time.Unix(0, 0), time.Unix(0, 0), time.Unix(0, 0).Add(tt.collectorTo))
			var out bytes.Buffer
			printStartupHealth(&out, spec.ShapeContainer, startup, 15*time.Second, 30*time.Second)
			gotWarning := strings.Contains(out.String(), "warning: collector startup exceeded")
			if gotWarning != tt.wantWarning {
				t.Errorf("warning = %t, want %t; output:\n%s", gotWarning, tt.wantWarning, out.String())
			}
		})
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWriteRunCompletionReturnsWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	for _, skipLoad := range []bool{false, true} {
		err := writeRunCompletion(failingWriter{err: writeErr}, skipLoad)
		if !errors.Is(err, writeErr) {
			t.Errorf("writeRunCompletion(skipLoad=%t) error = %v, want wrapped write error", skipLoad, err)
		}
	}
}

// takeSnapshot must stamp Snapshot.At after both scrapes return, so a slow
// (but successful) scrape does not produce a timestamp earlier than when the
// samples were actually captured. A window built from such snapshots would
// otherwise under-count its duration and inflate throughput.
func TestTakeSnapshotStampsAfterScrapes(t *testing.T) {
	const delay = 40 * time.Millisecond
	before := time.Now()
	snap, collErr, sinkErr := takeSnapshot(
		func() (report.Samples, error) {
			time.Sleep(delay)
			return report.Samples{{Name: "x", Value: 1}}, nil
		},
		func() (report.Samples, error) { return report.Samples{}, nil },
	)
	if collErr != nil || sinkErr != nil {
		t.Fatalf("unexpected scrape errors: coll=%v sink=%v", collErr, sinkErr)
	}
	if snap.At.Sub(before) < delay {
		t.Errorf("snapshot At=%v is before the scrape completed (started %v, delay %v)", snap.At, before, delay)
	}
	if len(snap.Collector) != 1 {
		t.Errorf("collector samples = %d, want 1", len(snap.Collector))
	}
}

// A scrape error must be returned (not fatal) so the caller can log it while
// still producing a timestamped snapshot for the successful side.
func TestTakeSnapshotReturnsScrapeError(t *testing.T) {
	wantErr := fmt.Errorf("proxy timeout")
	snap, collErr, sinkErr := takeSnapshot(
		func() (report.Samples, error) { return nil, wantErr },
		func() (report.Samples, error) { return report.Samples{{Name: "y", Value: 2}}, nil },
	)
	if collErr == nil {
		t.Fatal("expected collector scrape error")
	}
	if sinkErr != nil {
		t.Errorf("unexpected sink error: %v", sinkErr)
	}
	if snap.At.IsZero() {
		t.Error("snapshot At should be stamped even when a scrape fails")
	}
	if len(snap.Collector) != 0 || len(snap.Sink) != 1 {
		t.Errorf("samples = coll %d sink %d, want 0 and 1", len(snap.Collector), len(snap.Sink))
	}
}

func TestApplyLoadOverridesValidation(t *testing.T) {
	base := profile.Nemotron()

	valid := []struct {
		name string
		cfg  runConfig
	}{
		{"defaults untouched", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: -1}},
		{"fraction lower bound", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: 0}},
		{"fraction upper bound", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: 1}},
		{"payload at max", runConfig{logsPerSec: -1, workers: -1, payloadBytes: loadgen.MaxLogPayloadBytes, largeRecordFraction: -1}},
	}
	for _, tc := range valid {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			if _, err := applyLoadOverrides(base, tc.cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		cfg  runConfig
	}{
		{"fraction above one", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: 1.5}},
		{"fraction NaN", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: math.NaN()}},
		{"fraction +Inf", runConfig{logsPerSec: -1, workers: -1, payloadBytes: -1, largeRecordFraction: math.Inf(1)}},
		{"payload above max", runConfig{logsPerSec: -1, workers: -1, payloadBytes: loadgen.MaxLogPayloadBytes + 1, largeRecordFraction: -1}},
	}
	for _, tc := range invalid {
		t.Run("err/"+tc.name, func(t *testing.T) {
			if _, err := applyLoadOverrides(base, tc.cfg); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
