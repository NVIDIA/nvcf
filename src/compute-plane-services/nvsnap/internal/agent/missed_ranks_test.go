// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func names(m map[*corev1.Pod]string) map[string]string {
	out := map[string]string{}
	for p, why := range m {
		out[p.Name] = why
	}
	return out
}

// The ranks of an instance without nvsnap's identity are found, and why;
// what is not a rank of it is not.
func TestMissedRanks(t *testing.T) {
	leader := stsRank("vllm-0", "0", "8", "sts-a", true)
	mutated := stsRank("vllm-1", "1", "8", "sts-a", true)
	missed := stsRank("vllm-3", "", "", "sts-a", true)
	missed.Annotations = nil
	optedOut := stsRank("vllm-4", "", "", "sts-a", true)
	optedOut.Annotations = nil
	optedOut.Labels = map[string]string{webhook.InjectLabel: webhook.InjectDisabled}
	placeholder := stsRank("vllm-5", "", "", "sts-a", true)
	placeholder.Annotations = map[string]string{webhook.CRIURestoreAnnotation: "r5__1"}
	admitted := stsRank("vllm-6", "", "", "sts-a", true)
	admitted.Annotations = map[string]string{webhook.AdmittedConfigAnnotation: "cache://k1"}
	companion := stsRank("nats-0", "", "", "sts-a", true)
	companion.Annotations = nil
	companion.Spec.Containers = []corev1.Container{{Name: "nats"}}
	otherInstance := stsRank("other-3", "", "", "sts-b", true)
	otherInstance.Annotations = nil
	deleting := stsRank("vllm-7", "", "", "sts-a", true)
	deleting.Annotations = nil
	deleting.DeletionTimestamp = &metav1.Time{}
	done := stsRank("vllm-2", "", "", "sts-a", true)
	done.Annotations = nil
	done.Status.Phase = corev1.PodSucceeded

	pods := []corev1.Pod{leader, mutated, missed, optedOut, placeholder, admitted, companion, otherInstance, deleting, done}
	got := names(missedRanks(pods, &leader, 8))
	want := map[string]string{"vllm-3": missedNotAdmitted, "vllm-4": missedOptedOut}
	if len(got) != len(want) || got["vllm-3"] != want["vllm-3"] || got["vllm-4"] != want["vllm-4"] {
		t.Fatalf("missed %v, want %v", got, want)
	}
	// One pod is an instance of its own: its controller's other pods are
	// other instances.
	if got := missedRanks(pods, &leader, 1); len(got) != 0 {
		t.Fatalf("an instance of one pod has missed ranks %v", names(got))
	}
}

// Each missed rank is said once, however often the capture is tried: one
// warning, one event, one count.
func TestReportMissedOnce(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-missed")
	leader := stsRank("vllm-0", "0", "2", "sts-a", true)
	missed := stsRank("vllm-1", "", "", "sts-a", true)
	missed.Annotations = nil
	pods := []corev1.Pod{leader, missed}
	before := metricValue(t, metrics.Admissions, map[string]string{"decision": metrics.AdmitMissed, "reason": missedNotAdmitted})
	for i := 0; i < 3; i++ {
		if !c.considerMissed(pods, &leader, 2, quietEntry()) {
			t.Fatal("an instance with a missed rank reported as capturable")
		}
	}
	if got := metricValue(t, metrics.Admissions, map[string]string{"decision": metrics.AdmitMissed, "reason": missedNotAdmitted}) - before; got != 1 {
		t.Fatalf("counted %v times, want once", got)
	}
	e := event(rec)
	if !strings.Contains(e, "Warning "+eventNotAdmitted) || !strings.Contains(e, "without the nvsnap webhook") {
		t.Fatalf("event %q", e)
	}
	if e := event(rec); e != "" {
		t.Fatalf("a second event %q", e)
	}
	// Every rank mutated: nothing to say, the capture goes on.
	ok := stsRank("vllm-1", "1", "2", "sts-a", true)
	if c.considerMissed([]corev1.Pod{leader, ok}, &leader, 2, quietEntry()) {
		t.Fatal("an instance whose ranks were all admitted reported a missed rank")
	}
}

// A rank that opts out is said so, not blamed on the webhook.
func TestReportMissedOptedOut(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-optout")
	leader := stsRank("vllm-0", "0", "2", "sts-a", true)
	out := stsRank("vllm-1", "", "", "sts-a", true)
	out.Annotations = nil
	out.Labels = map[string]string{webhook.InjectLabel: webhook.InjectDisabled}
	c.considerMissed([]corev1.Pod{leader, out}, &leader, 2, quietEntry())
	if e := event(rec); !strings.Contains(e, "opts out") {
		t.Fatalf("event %q", e)
	}
}
