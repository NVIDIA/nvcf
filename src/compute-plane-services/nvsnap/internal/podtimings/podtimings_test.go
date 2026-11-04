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

package podtimings

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helper: build a pod with the given annotations + ready transition.
// readyAfter=0 means not-ready. Created/ready times are absolute.
func mkPod(name string, ann map[string]string, created time.Time, readyAfter time.Duration) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "ns1",
			Annotations:       ann,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
	}
	if readyAfter > 0 {
		p.Status.Conditions = []corev1.PodCondition{
			{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(created.Add(readyAfter)),
			},
		}
	}
	return p
}

func TestClassify_ColdStart(t *testing.T) {
	created := time.Date(2026, 5, 31, 22, 50, 6, 0, time.UTC)
	p := mkPod("0-sr-cold", nil, created, 188*time.Second)

	timings := Compute([]corev1.Pod{p})
	if len(timings) != 1 {
		t.Fatalf("got %d, want 1", len(timings))
	}
	got := timings[0]
	if got.Mode != ModeColdStart {
		t.Errorf("Mode = %q, want cold-start", got.Mode)
	}
	if got.Hash != "" {
		t.Errorf("Hash = %q, want empty for cold start", got.Hash)
	}
	if got.Duration != 188*time.Second {
		t.Errorf("Duration = %v, want 188s", got.Duration)
	}
	if got.NotReady {
		t.Errorf("NotReady = true, want false (pod is Ready)")
	}
}

func TestClassify_Restored(t *testing.T) {
	created := time.Date(2026, 5, 31, 22, 56, 0, 0, time.UTC)
	p := mkPod("0-sr-restored", map[string]string{
		RestoreFromAnnotation:       "85ec4d75ee57c1be444dd19733f63cfd8d93ccb3cdd44aff5a8e14ebef9e5d98",
		FunctionVersionIDAnnotation: "cd1116dc-2305-4478-949c-1ed4c806bc09",
	}, created, 43*time.Second)

	timings := Compute([]corev1.Pod{p})
	got := timings[0]
	if got.Mode != ModeRestored {
		t.Errorf("Mode = %q, want restored", got.Mode)
	}
	// Short hash, not full
	if got.Hash != "85ec4d75ee57c1be" {
		t.Errorf("Hash = %q, want 85ec4d75ee57c1be (16-char short)", got.Hash)
	}
	if got.FunctionVersionID != "cd1116dc-2305-4478-949c-1ed4c806bc09" {
		t.Errorf("FunctionVersionID = %q", got.FunctionVersionID)
	}
	if got.Duration != 43*time.Second {
		t.Errorf("Duration = %v, want 43s", got.Duration)
	}
}

func TestClassify_EmptyRestoreFromAnnotationIsColdStart(t *testing.T) {
	// Defense in depth: if Hook A stamped an empty annotation (the
	// nvca#14 scenario before defense-in-depth landed), we should
	// classify the pod as cold-start, not as "restored with empty hash".
	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	p := mkPod("0-sr-emptyann", map[string]string{
		RestoreFromAnnotation: "", // empty on purpose
	}, created, 188*time.Second)

	timings := Compute([]corev1.Pod{p})
	if timings[0].Mode != ModeColdStart {
		t.Errorf("Mode = %q, want cold-start (empty annotation is not a real restore)", timings[0].Mode)
	}
}

func TestClassify_NotReady(t *testing.T) {
	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	p := mkPod("0-sr-pending", nil, created, 0) // readyAfter=0 → no Ready condition

	timings := Compute([]corev1.Pod{p})
	got := timings[0]
	if !got.NotReady {
		t.Error("NotReady should be true for pod without Ready=True condition")
	}
	if got.Duration != 0 {
		t.Errorf("Duration = %v, want 0 for not-ready", got.Duration)
	}
	if got.ReadyAt != nil {
		t.Errorf("ReadyAt should be nil for not-ready pod")
	}
}

func TestClassify_ReadyEqualsFalseIsNotReady(t *testing.T) {
	// A pod with Ready=False (or Unknown) is also not-ready.
	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	p := mkPod("0-sr-failing", nil, created, 0)
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(created.Add(60 * time.Second))},
	}
	timings := Compute([]corev1.Pod{p})
	if !timings[0].NotReady {
		t.Error("Ready=False should be treated as not-ready")
	}
}

func TestCompute_SortsOldestFirst(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	pods := []corev1.Pod{
		mkPod("c", nil, t0.Add(2*time.Second), 10*time.Second),
		mkPod("a", nil, t0, 10*time.Second),
		mkPod("b", nil, t0.Add(time.Second), 10*time.Second),
	}
	got := Compute(pods)
	if got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Errorf("sort wrong: %v", []string{got[0].Name, got[1].Name, got[2].Name})
	}
}

func TestCompute_SkipsPodsWithoutCreationTimestamp(t *testing.T) {
	// A naked Pod{} has zero CreationTimestamp. Compute must skip it
	// rather than emit a row with Duration=now (garbage).
	got := Compute([]corev1.Pod{{}})
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestCompute_StableOrderForSameCreationTimestamp(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	pods := []corev1.Pod{
		mkPod("z", nil, t0, 10*time.Second),
		mkPod("a", nil, t0, 10*time.Second),
		mkPod("m", nil, t0, 10*time.Second),
	}
	got := Compute(pods)
	// Same creation → secondary key is Name asc.
	if got[0].Name != "a" || got[1].Name != "m" || got[2].Name != "z" {
		t.Errorf("name-secondary sort wrong: %v", []string{got[0].Name, got[1].Name, got[2].Name})
	}
}

// ---------------- Summary ----------------

func TestSummary_BothModes(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	pods := []corev1.Pod{
		mkPod("cold", nil, t0, 188*time.Second),
		mkPod("r1", map[string]string{RestoreFromAnnotation: "h1234567890abcdef"}, t0, 40*time.Second),
		mkPod("r2", map[string]string{RestoreFromAnnotation: "h1234567890abcdef"}, t0, 50*time.Second),
		mkPod("r3", map[string]string{RestoreFromAnnotation: "h1234567890abcdef"}, t0, 45*time.Second),
		mkPod("inflight", nil, t0, 0),
	}
	timings := Compute(pods)
	s := Summary(timings)

	if s.Total != 5 {
		t.Errorf("Total = %d, want 5", s.Total)
	}
	if s.Cold != 1 || s.Restored != 3 || s.NotReady != 1 {
		t.Errorf("counts wrong: cold=%d restored=%d notReady=%d", s.Cold, s.Restored, s.NotReady)
	}
	if s.AvgCold != 188*time.Second {
		t.Errorf("AvgCold = %v, want 188s", s.AvgCold)
	}
	if s.AvgRestored != 45*time.Second {
		t.Errorf("AvgRestored = %v, want 45s (avg of 40,50,45)", s.AvgRestored)
	}
	if s.AvgSavings != 143*time.Second {
		t.Errorf("AvgSavings = %v, want 143s (188-45)", s.AvgSavings)
	}
	if s.TotalSavings != 3*143*time.Second {
		t.Errorf("TotalSavings = %v, want 7m9s (3 × 143s)", s.TotalSavings)
	}
	want := 188.0 / 45.0
	if s.SpeedupX < want-0.05 || s.SpeedupX > want+0.05 {
		t.Errorf("SpeedupX = %.2f, want %.2f", s.SpeedupX, want)
	}
}

func TestSummary_OnlyCold(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("c1", nil, t0, 100*time.Second),
		mkPod("c2", nil, t0, 120*time.Second),
	})
	s := Summary(timings)
	if s.AvgRestored != 0 {
		t.Errorf("AvgRestored should be 0 when no restored pods; got %v", s.AvgRestored)
	}
	if s.SpeedupX != 0 {
		t.Errorf("SpeedupX should be 0 when no restored pods; got %v", s.SpeedupX)
	}
	if s.TotalSavings != 0 {
		t.Errorf("TotalSavings should be 0; got %v", s.TotalSavings)
	}
}

func TestSummary_OnlyRestored(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("r1", map[string]string{RestoreFromAnnotation: "h1"}, t0, 40*time.Second),
		mkPod("r2", map[string]string{RestoreFromAnnotation: "h1"}, t0, 50*time.Second),
	})
	s := Summary(timings)
	if s.AvgCold != 0 || s.SpeedupX != 0 || s.TotalSavings != 0 {
		t.Errorf("savings should be 0 when no baseline cold-start; got %+v", s)
	}
}

func TestSummary_RestoredFasterButGuardedAgainstNegativeSavings(t *testing.T) {
	// Pathological case: average restored is SLOWER than cold-start.
	// AvgSavings should be 0, not negative.
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("c1", nil, t0, 30*time.Second),
		mkPod("r1", map[string]string{RestoreFromAnnotation: "h1"}, t0, 90*time.Second),
	})
	s := Summary(timings)
	if s.AvgSavings != 0 {
		t.Errorf("AvgSavings should clamp to 0 when restored slower; got %v", s.AvgSavings)
	}
	if s.SpeedupX != 0 {
		t.Errorf("SpeedupX should be 0 when restored slower; got %v", s.SpeedupX)
	}
}

func TestSummaryByFunction_GroupsByFvID(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	ann := func(rf, fvID string) map[string]string {
		return map[string]string{
			RestoreFromAnnotation:       rf,
			FunctionVersionIDAnnotation: fvID,
		}
	}
	timings := Compute([]corev1.Pod{
		mkPod("fv-a-cold", map[string]string{FunctionVersionIDAnnotation: "fv-a"}, t0, 100*time.Second),
		mkPod("fv-a-r", ann("h-a", "fv-a"), t0, 30*time.Second),
		mkPod("fv-b-cold", map[string]string{FunctionVersionIDAnnotation: "fv-b"}, t0, 200*time.Second),
	})
	groups := SummaryByFunction(timings)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	// Sorted by fvID
	if groups[0].FunctionVersionID != "fv-a" || groups[1].FunctionVersionID != "fv-b" {
		t.Errorf("group order wrong: %v", groups)
	}
	if groups[0].Stats.Restored != 1 || groups[0].Stats.Cold != 1 {
		t.Errorf("fv-a counts wrong: %+v", groups[0].Stats)
	}
	if groups[1].Stats.Cold != 1 || groups[1].Stats.Restored != 0 {
		t.Errorf("fv-b counts wrong: %+v", groups[1].Stats)
	}
}

func TestSummaryByFunction_UnattributedBucket(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("nofvid", nil, t0, 100*time.Second),
	})
	groups := SummaryByFunction(timings)
	if len(groups) != 1 || groups[0].FunctionVersionID != "" {
		t.Errorf("unattributed bucket should have empty fvID; got %v", groups)
	}
}

// ---------------- Render ----------------

func TestRenderTable_GoldenFile(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("0-sr-1a9b3b32-cold-start-pod", map[string]string{
			// fvID is stamped on the cold pod too — it triggered the
			// initial capture for this function-version. Required for
			// speedup math to apply per-fvID.
			FunctionVersionIDAnnotation: "cd1116dc-2305-4478-949c-1ed4c806bc09",
			CheckpointOnWarmAnnotation:  "true",
		}, t0, 188*time.Second),
		mkPod("0-sr-68599d45-restored-pod-1", map[string]string{
			RestoreFromAnnotation:       "85ec4d75ee57c1be444dd19733f63cfd8d93ccb3cdd44aff5a8e14ebef9e5d98",
			FunctionVersionIDAnnotation: "cd1116dc-2305-4478-949c-1ed4c806bc09",
		}, t0.Add(time.Minute), 43*time.Second),
		mkPod("0-sr-71ec9322-restored-pod-2", map[string]string{
			RestoreFromAnnotation:       "85ec4d75ee57c1be444dd19733f63cfd8d93ccb3cdd44aff5a8e14ebef9e5d98",
			FunctionVersionIDAnnotation: "cd1116dc-2305-4478-949c-1ed4c806bc09",
		}, t0.Add(time.Minute), 45*time.Second),
	})

	var buf bytes.Buffer
	if err := RenderTable(&buf, timings, RenderOptions{}); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	got := buf.String()

	// Lenient golden: assert presence of headline elements (full
	// tabwriter layout depends on terminal width assumptions that
	// would make a strict-match test brittle).
	wants := []string{
		"NAME",
		"MODE",
		"READY-IN",
		"HASH",
		"FVID",
		"0-sr-1a9b3b32-cold-start-pod",
		"cold-start",
		"3m08s",
		"restored",
		"43s",
		"85ec4d75ee57c1be",
		"cd1116dc...", // shortFVID
		"SUMMARY",
		"1 cold (avg 3m08s)",
		"2 restored (avg 44s)",
		"faster",
		"saved",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("table missing %q\n----\n%s", w, got)
		}
	}
}

func TestRenderTable_EmptyInput(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderTable(&buf, nil, RenderOptions{}); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	if !strings.Contains(buf.String(), "No pods found") {
		t.Errorf("empty input should print friendly message; got %q", buf.String())
	}
}

func TestRenderTable_ShowNamespaceAndNode(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{mkPod("p1", nil, t0, 10*time.Second)})

	var buf bytes.Buffer
	if err := RenderTable(&buf, timings, RenderOptions{ShowNamespace: true, ShowNode: true}); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}
	out := buf.String()
	for _, w := range []string{"NAMESPACE", "NODE", "ns1", "node-1"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q\n----\n%s", w, out)
		}
	}
}

func TestRenderJSON_ShapeIsStable(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	timings := Compute([]corev1.Pod{
		mkPod("p-cold", nil, t0, 100*time.Second),
		mkPod("p-restored", map[string]string{
			RestoreFromAnnotation:       "h1234567890abcdef",
			FunctionVersionIDAnnotation: "fv-1",
		}, t0, 30*time.Second),
	})
	var buf bytes.Buffer
	if err := RenderJSON(&buf, timings); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var doc struct {
		Timings []PodTiming     `json:"timings"`
		Summary Stats           `json:"summary"`
		Groups  []FunctionGroup `json:"groups"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Timings) != 2 {
		t.Errorf("timings = %d, want 2", len(doc.Timings))
	}
	if doc.Summary.Cold != 1 || doc.Summary.Restored != 1 {
		t.Errorf("summary counts wrong: %+v", doc.Summary)
	}
}

// ---------------- formatDuration ----------------

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"}, // negative clamps to 0
		{43 * time.Second, "43s"},
		{60 * time.Second, "1m"},
		{188 * time.Second, "3m08s"},
		{3600 * time.Second, "1h"},
		{(3600 + 14*60) * time.Second, "1h14m"},
		{(3600 + 14*60 + 30) * time.Second, "1h14m"}, // sub-minute drops
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShortFVID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"cd1116dc-2305-4478-949c-1ed4c806bc09", "cd1116dc..."},
		{"short", "short"},
		{"12345678", "12345678"},
		{"123456789", "12345678"}, // not a UUID (no dash at position 8), trimmed without ellipsis
		{"", ""},
	}
	for _, tc := range cases {
		if got := shortFVID(tc.in); got != tc.want {
			t.Errorf("shortFVID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
