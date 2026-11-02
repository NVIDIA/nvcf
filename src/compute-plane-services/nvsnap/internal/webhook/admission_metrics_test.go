// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
)

// admissions is the count of the webhook's decision with reason.
func admissions(t *testing.T, decision, reason string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.Admissions)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if got["decision"] == decision && got["reason"] == reason {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// Each decision for a rank of a captured instance is counted once, with
// why it started cold.
func TestCRIUGroupRestore_CountsAdmissions(t *testing.T) {
	two := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1"}, Nodes: []string{"n0", "n1"}}
	three := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1", "r2__1"}}
	for name, tc := range map[string]struct {
		g        CRIUGroup
		blocked  string
		refuse   bool
		decision string
		reason   string
	}{
		"restored":                {g: two, decision: metrics.AdmitRestore},
		"different instance size": {g: three, decision: metrics.AdmitCold, reason: "size-mismatch"},
		"a blocked checkpoint":    {g: two, blocked: "r0__1", decision: metrics.AdmitCold, reason: "checkpoint-blocked"},
		"refused as a group":      {g: two, refuse: true, decision: metrics.AdmitCold, reason: "not-restorable-as-group"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := groupMutator(t, tc.g, tc.blocked)
			if tc.refuse {
				m.CRIUGroupAdmits = func(context.Context, *corev1.Pod) (bool, string) { return false, "a rank already runs" }
			}
			before := admissions(t, tc.decision, tc.reason)
			m.criuGroupRestorePatches(context.Background(), lwsRank("0"))
			if got := admissions(t, tc.decision, tc.reason) - before; got != 1 {
				t.Fatalf("%s/%q counted %v times, want 1", tc.decision, tc.reason, got)
			}
		})
	}
}

// A pod of no captured configuration is not a decision to count.
func TestCRIUGroupRestore_NoCaptureNotCounted(t *testing.T) {
	m, _ := groupMutator(t, CRIUGroup{}, "")
	m.CRIUGroups = func(context.Context, string, string) (CRIUGroup, bool) { return CRIUGroup{}, false }
	before := admissions(t, metrics.AdmitRestore, "") + admissions(t, metrics.AdmitCold, "size-mismatch")
	m.criuGroupRestorePatches(context.Background(), lwsRank("0"))
	if admissions(t, metrics.AdmitRestore, "")+admissions(t, metrics.AdmitCold, "size-mismatch") != before {
		t.Fatal("a pod without a capture was counted")
	}
}

// A pod of a configuration that falls back is counted with the fallback's
// reason; one CRIU serves is not counted as a fallback.
func TestCRIUFallback_CountsAdmissions(t *testing.T) {
	m, pod, _ := fallbackModelMutator(t, CRIUFallback{Reason: "gpushare-failed"}, true)
	before := admissions(t, metrics.AdmitFallback, "gpushare-failed")
	admit(t, m, pod)
	if got := admissions(t, metrics.AdmitFallback, "gpushare-failed") - before; got != 1 {
		t.Fatalf("fallback counted %v times, want 1", got)
	}
	m, pod, _ = fallbackModelMutator(t, CRIUFallback{}, false)
	before = admissions(t, metrics.AdmitFallback, "")
	admit(t, m, pod)
	if admissions(t, metrics.AdmitFallback, "") != before {
		t.Fatal("a configuration CRIU serves was counted as a fallback")
	}
}
