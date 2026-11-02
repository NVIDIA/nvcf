// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// series returns, for each series of c, its label values (in any order)
// and its value: a counter's or gauge's value, a histogram's number of
// observations.
func series(t *testing.T, c prometheus.Collector) map[*[]string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[*[]string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var vals []string
			for _, l := range m.GetLabel() {
				vals = append(vals, l.GetValue())
			}
			v := m.GetCounter().GetValue() + m.GetGauge().GetValue() + float64(m.GetHistogram().GetSampleCount())
			out[&vals] = v
		}
	}
	return out
}

// value is the value of c's series with label values labels (see series);
// 0 when it has none.
func value(t *testing.T, c prometheus.Collector, labels ...string) float64 {
	t.Helper()
	for vals, v := range series(t, c) {
		if sameSet(*vals, labels) {
			return v
		}
	}
	return 0
}

// samples is the number of observations of a histogram series.
func samples(t *testing.T, h *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	return uint64(value(t, h, labels...))
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestRanks(t *testing.T) {
	for n, want := range map[int]string{-1: "unknown", 0: "unknown", 1: "1", 2: "2-4", 4: "2-4", 5: "5-8", 8: "5-8", 9: "9-16", 16: "9-16", 17: "17+", 64: "17+"} {
		if got := Ranks(n); got != want {
			t.Errorf("Ranks(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestGPU(t *testing.T) {
	if got := GPU(" NVIDIA-GB300 "); got != "NVIDIA-GB300" {
		t.Errorf("GPU = %q", got)
	}
	if got := GPU(""); got != "unknown" {
		t.Errorf("GPU(\"\") = %q, want unknown", got)
	}
	if got := GPU(strings.Repeat("x", 200)); len(got) != labelMax {
		t.Errorf("GPU of a long name has %d bytes, want %d", len(got), labelMax)
	}
}

// Failure texts as the agent writes them map to a fixed set of classes.
func TestReason(t *testing.T) {
	for text, want := range map[string]string{
		"": "",
		"preflight: mini-service-nats-5c4d577bb5-kv6rh: process 3438359 (nats-server) holds MPTCP sockets (fd 4, fd 7)": "mptcp",
		"preflight: engine: /dev/infiniband/uverbs0 is open":                                                            "rdma",
		"gpushare suspend: CUDA_ERROR_UNKNOWN":                                                                          "gpushare",
		"the checkpoint could not be brought to this node: ensure-local cascade failed":                                 "fetch",
		"context deadline exceeded":                                                                                     "timeout",
		"CRIU dump failed (criu-v2): exit status 1":                                                                     "criu",
		"the restored engine never served":                                                                              "other",
	} {
		if got := Reason(text); got != want {
			t.Errorf("Reason(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestObserveCapture(t *testing.T) {
	labels := []string{MethodCRIU, CaptureRefused, "mptcp", "GB300-t", "5-8"}
	before := value(t, CaptureOutcomes, labels...)
	ObserveCapture(MethodCRIU, CaptureRefused, "mptcp", "GB300-t", 8)
	if got := value(t, CaptureOutcomes, labels...) - before; got != 1 {
		t.Fatalf("refusals counted %v, want 1", got)
	}
}

func TestObservePhasesSkipNegative(t *testing.T) {
	before := samples(t, CapturePhaseSeconds, MethodCRIU, "dump", "neg-t", "1")
	ObserveCapturePhase(MethodCRIU, "dump", "neg-t", 1, -time.Second)
	ObserveCapturePhase(MethodCRIU, "dump", "neg-t", 1, 3*time.Second)
	if got := samples(t, CapturePhaseSeconds, MethodCRIU, "dump", "neg-t", "1") - before; got != 1 {
		t.Fatalf("observed %d, want 1 (the negative one dropped)", got)
	}
	before = samples(t, RestorePhaseSeconds, MethodCRIU, "ready", "neg-t", "2-4")
	ObserveRestorePhase(MethodCRIU, "ready", "neg-t", 3, -time.Second)
	ObserveRestorePhase(MethodCRIU, "ready", "neg-t", 3, time.Second)
	if got := samples(t, RestorePhaseSeconds, MethodCRIU, "ready", "neg-t", "2-4") - before; got != 1 {
		t.Fatalf("observed %d, want 1", got)
	}
}

func TestObserveCaptureBytesSkipsEmpty(t *testing.T) {
	before := samples(t, CaptureBytes, MethodCRIU, "rootfs", "bytes-t")
	ObserveCaptureBytes(MethodCRIU, "rootfs", "bytes-t", 0)
	ObserveCaptureBytes(MethodCRIU, "rootfs", "bytes-t", 4096)
	if got := samples(t, CaptureBytes, MethodCRIU, "rootfs", "bytes-t") - before; got != 1 {
		t.Fatalf("observed %d, want 1", got)
	}
}

// A failed fetch is counted but its time is not: it says nothing about the
// source's speed.
func TestObserveFetch(t *testing.T) {
	okBefore, failedBefore := value(t, RestoreFetches, "rails", "ok"), value(t, RestoreFetches, "rails", "failed")
	timed := samples(t, RestoreFetchSeconds, "rails")
	ObserveFetch("rails", true, 12*time.Second)
	ObserveFetch("rails", false, time.Second)
	if value(t, RestoreFetches, "rails", "ok")-okBefore != 1 || value(t, RestoreFetches, "rails", "failed")-failedBefore != 1 {
		t.Fatal("fetches not counted by result")
	}
	if got := samples(t, RestoreFetchSeconds, "rails") - timed; got != 1 {
		t.Fatalf("timed %d fetches, want 1", got)
	}
	before := value(t, RestoreFetches, "unknown", "failed")
	ObserveFetch("", false, 0)
	if value(t, RestoreFetches, "unknown", "failed")-before != 1 {
		t.Fatal("a fetch from no known source not counted as unknown")
	}
}

func TestObservePodReady(t *testing.T) {
	before := samples(t, PodReadySeconds, MethodCold, "ready-t", "1")
	ObservePodReady(MethodCold, "ready-t", 1, 95*time.Second)
	if got := samples(t, PodReadySeconds, MethodCold, "ready-t", "1") - before; got != 1 {
		t.Fatalf("observed %d, want 1", got)
	}
}

func TestCountAdmission(t *testing.T) {
	before := value(t, Admissions, AdmitCold, "size-mismatch")
	CountAdmission(AdmitCold, "size-mismatch")
	if value(t, Admissions, AdmitCold, "size-mismatch")-before != 1 {
		t.Fatal("admission not counted")
	}
}

// Only the current bundle is marked.
func TestSetBuildInfo(t *testing.T) {
	SetBuildInfo("v1")
	SetBuildInfo("v2")
	if n := len(series(t, BuildInfo)); n != 1 {
		t.Fatalf("%d build_info series, want 1", n)
	}
	if value(t, BuildInfo, "v2") != 1 {
		t.Fatal("v2 not marked")
	}
	SetBuildInfo("")
	if value(t, BuildInfo, "unknown") != 1 {
		t.Fatal("an unknown bundle not marked unknown")
	}
}

// The agent exposes the lifecycle series.
func TestRegisterAgentRegistersLifecycle(t *testing.T) {
	RegisterAgent()
	for _, c := range lifecycleCollectors {
		if err := prometheus.Register(c); err == nil {
			t.Fatalf("%v was not registered by RegisterAgent", c)
		} else if !errors.As(err, new(prometheus.AlreadyRegisteredError)) {
			t.Fatalf("register: %v", err)
		}
	}
}
