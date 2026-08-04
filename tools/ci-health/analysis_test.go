// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

// Every case here corresponds to a defect this tool actually shipped with. The
// analysis functions are pure, so none of this touches the network.

func at(day, hour int) time.Time {
	return time.Date(2026, 7, day, hour, 0, 0, 0, time.UTC)
}

func tp(day, hour, minute int) *time.Time {
	t := time.Date(2026, 7, day, hour, minute, 0, 0, time.UTC)
	return &t
}

func mkJob(name string, created, started, completed *time.Time, conclusion string, runID int64, runner string) Job {
	return Job{
		Name:        name,
		RunID:       runID,
		CreatedAt:   created,
		StartedAt:   started,
		CompletedAt: completed,
		Conclusion:  conclusion,
		RunnerName:  runner,
	}
}

func TestPercentileInterpolatesBetweenSamples(t *testing.T) {
	// Index truncation would return 10; the real p90 is 9.1.
	got := pctl([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.9)
	if math.Abs(got-9.1) > 1e-9 {
		t.Fatalf("p90 = %v, want 9.1", got)
	}
}

func TestPercentileMedianOfEvenLength(t *testing.T) {
	if got := pctl([]float64{1, 2, 3, 4}, 0.5); math.Abs(got-2.5) > 1e-9 {
		t.Fatalf("median = %v, want 2.5", got)
	}
}

func TestPercentileDegenerateInputs(t *testing.T) {
	if got := pctl(nil, 0.5); got != 0 {
		t.Fatalf("empty = %v, want 0", got)
	}
	if got := pctl([]float64{7.5}, 0.9); got != 7.5 {
		t.Fatalf("single = %v, want 7.5", got)
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	in := []float64{3, 1, 2}
	pctl(in, 0.5)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Fatalf("input was reordered: %v", in)
	}
}

// weeklyFixture has a truncated tail in W28, then two whole weeks.
func weeklyFixture() []Run {
	runs := []Run{{Start: at(6, 9), Minutes: 1}}
	for i := 0; i < 5; i++ {
		runs = append(runs, Run{Start: at(13, 9), Minutes: 10})
	}
	for i := 0; i < 5; i++ {
		runs = append(runs, Run{Start: at(20, 9), Minutes: 20})
	}
	return runs
}

func TestWeeklyKeepsEveryWeekWhenNotTruncated(t *testing.T) {
	weeks := weekly(weeklyFixture(), false)
	if len(weeks) != 3 {
		t.Fatalf("got %d weeks, want 3", len(weeks))
	}
	if weeks[0].Label != "2026-W28" || weeks[0].P50 != 1 {
		t.Fatalf("first week = %+v", weeks[0])
	}
}

func TestWeeklyDropsPartialOldestWeekWhenTruncated(t *testing.T) {
	weeks := weekly(weeklyFixture(), true)
	if len(weeks) != 2 {
		t.Fatalf("got %d weeks, want 2", len(weeks))
	}
	if weeks[0].Label != "2026-W29" {
		t.Fatalf("first week = %q, want 2026-W29", weeks[0].Label)
	}
}

func TestWeeklyNeverDropsTheOnlyWeek(t *testing.T) {
	if got := weekly([]Run{{Start: at(20, 9), Minutes: 4}}, true); len(got) != 1 {
		t.Fatalf("got %d weeks, want 1", len(got))
	}
}

func TestWeeklyReportsRunCounts(t *testing.T) {
	weeks := weekly(weeklyFixture(), true)
	for _, w := range weeks {
		if w.Count != 5 {
			t.Fatalf("week %s count = %d, want 5", w.Label, w.Count)
		}
	}
}

func TestSkippedMatrixDetection(t *testing.T) {
	cases := []struct {
		name     string
		job      Job
		wantSkip bool
	}{
		// A row skipped by change detection keeps its raw matrix expression.
		{"unexpanded expression", Job{Name: "bazel (${{ matrix.subtree.id }})", Conclusion: "success"}, true},
		{"explicit skip", Job{Name: "bazel (nvca)", Conclusion: "skipped"}, true},
		{"real row", Job{Name: "bazel (nvca)", Conclusion: "success"}, false},
	}
	for _, c := range cases {
		if got := isSkippedMatrix(c.job); got != c.wantSkip {
			t.Errorf("%s: isSkippedMatrix = %v, want %v", c.name, got, c.wantSkip)
		}
	}
}

func TestGateJobDetection(t *testing.T) {
	for name, want := range map[string]bool{
		"detect changed subtrees": true,
		"bazel required checks":   true,
		"bazel (nvca)":            false,
	} {
		if got := isGate(name); got != want {
			t.Errorf("isGate(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAnalyseJobsSplitsQueueFromExecution(t *testing.T) {
	stats := analyseJobs([]Job{
		mkJob("bazel (nvca)", tp(20, 10, 0), tp(20, 10, 2), tp(20, 10, 12), "success", 1, "self"),
	})
	s := stats["bazel (nvca)"]
	if s.Runs != 1 {
		t.Fatalf("runs = %d, want 1", s.Runs)
	}
	if math.Abs(s.Queue[0]-2) > 1e-9 {
		t.Errorf("queue = %v, want 2", s.Queue[0])
	}
	if math.Abs(s.Exec[0]-10) > 1e-9 {
		t.Errorf("exec = %v, want 10", s.Exec[0])
	}
}

func TestAnalyseJobsCountsSkippedWithoutTiming(t *testing.T) {
	stats := analyseJobs([]Job{
		mkJob("bazel (nvca)", tp(20, 0, 0), tp(20, 0, 0), tp(20, 0, 0), "skipped", 1, "self"),
	})
	s := stats["bazel (nvca)"]
	if s.Skipped != 1 || s.Runs != 0 {
		t.Fatalf("skipped = %d, runs = %d; want 1, 0", s.Skipped, s.Runs)
	}
}

func TestAnalyseJobsDropsMissingTimestamps(t *testing.T) {
	stats := analyseJobs([]Job{
		mkJob("bazel (nvca)", tp(20, 0, 0), nil, tp(20, 1, 0), "success", 1, "self"),
	})
	if s := stats["bazel (nvca)"]; s.Runs != 0 {
		t.Fatalf("runs = %d, want 0", s.Runs)
	}
}

func TestAnalyseJobsTalliesHostedRunners(t *testing.T) {
	stats := analyseJobs([]Job{
		mkJob("gate", tp(20, 1, 0), tp(20, 1, 0), tp(20, 2, 0), "success", 1, "GitHub Actions 12"),
	})
	if s := stats["gate"]; s.Hosted != 1 {
		t.Fatalf("hosted = %d, want 1", s.Hosted)
	}
}

func TestLongPolesPicksLastFinishingBuildJob(t *testing.T) {
	tally, runs := longPoles([]Job{
		mkJob("bazel (fast)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 30), "success", 1, "self"),
		mkJob("bazel (slow)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 50), "success", 1, "self"),
	})
	if runs != 1 || tally["bazel (slow)"] != 1 || len(tally) != 1 {
		t.Fatalf("tally = %v over %d runs", tally, runs)
	}
}

func TestLongPolesExcludesGateJobs(t *testing.T) {
	// The required-checks gate finishes last by construction. It is not the
	// reason the build is slow.
	tally, _ := longPoles([]Job{
		mkJob("bazel (slow)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 50), "success", 1, "self"),
		mkJob("bazel required checks", tp(20, 10, 0), tp(20, 10, 0), tp(20, 11, 0), "success", 1, "self"),
	})
	if tally["bazel (slow)"] != 1 || len(tally) != 1 {
		t.Fatalf("tally = %v", tally)
	}
}

func TestLongPolesTalliesRunsIndependently(t *testing.T) {
	tally, runs := longPoles([]Job{
		mkJob("bazel (a)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 50), "success", 1, "self"),
		mkJob("bazel (b)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 30), "success", 1, "self"),
		mkJob("bazel (b)", tp(21, 10, 0), tp(21, 10, 0), tp(21, 10, 50), "success", 2, "self"),
		mkJob("bazel (a)", tp(21, 10, 0), tp(21, 10, 0), tp(21, 10, 30), "success", 2, "self"),
	})
	if runs != 2 || tally["bazel (a)"] != 1 || tally["bazel (b)"] != 1 {
		t.Fatalf("tally = %v over %d runs", tally, runs)
	}
}

func healthyCache() CacheState {
	return CacheState{TotalGB: 1, Count: 3, Families: map[string]float64{}, Copies: map[string]int{}, Dupes: map[string][]string{}}
}

func TestDiagnoseFlagsCachePressure(t *testing.T) {
	c := healthyCache()
	c.TotalGB, c.StrandedGB = 9.5, 1.9
	causes, _ := diagnose([]Run{{Start: at(20, 0), Minutes: 10}}, nil, nil, 0, c)
	if !hasCause(causes, "Cache pressure") {
		t.Fatalf("causes = %+v", causes)
	}
}

func TestDiagnoseQuietWhenHealthy(t *testing.T) {
	stats := analyseJobs([]Job{
		mkJob("bazel (nvca)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 30), "success", 1, "self"),
	})
	causes, wall := diagnose([]Run{{Start: at(20, 0), Minutes: 10}}, stats, nil, 0, healthyCache())
	if len(causes) != 0 {
		t.Fatalf("expected no causes, got %+v", causes)
	}
	if math.Abs(wall-10) > 1e-9 {
		t.Fatalf("wall = %v, want 10", wall)
	}
}

func TestDiagnoseWarnsOnHighSkipRate(t *testing.T) {
	var jobs []Job
	for i := 0; i < 9; i++ {
		jobs = append(jobs, mkJob("bazel (s)", tp(20, 0, 0), tp(20, 0, 0), tp(20, 0, 0), "skipped", 1, "self"))
	}
	jobs = append(jobs, mkJob("bazel (real)", tp(20, 10, 0), tp(20, 10, 0), tp(20, 10, 30), "success", 1, "self"))
	causes, _ := diagnose([]Run{{Start: at(20, 0), Minutes: 10}}, analyseJobs(jobs), nil, 0, healthyCache())
	found := false
	for _, c := range causes {
		if c.Note {
			found = true
		}
	}
	if !found {
		t.Fatalf("a 90%% skip rate must be called out; causes = %+v", causes)
	}
}

func TestDiagnoseRanksLargestContributorFirst(t *testing.T) {
	// A job that waits 10 min for a runner and executes for 1.
	stats := analyseJobs([]Job{
		mkJob("bazel (a)", tp(20, 10, 0), tp(20, 10, 10), tp(20, 10, 11), "success", 1, "self"),
	})
	causes, _ := diagnose([]Run{{Start: at(20, 0), Minutes: 20}}, stats, nil, 0, healthyCache())
	if len(causes) == 0 || causes[0].Title != "Runner queue wait" {
		t.Fatalf("first cause = %+v", causes)
	}
}

func hasCause(causes []Cause, title string) bool {
	for _, c := range causes {
		if c.Title == title {
			return true
		}
	}
	return false
}

func TestSummariseCachesFindsStrandedMergeQueueEntries(t *testing.T) {
	caches := []Cache{
		{Key: "bazel-root-abc", Ref: "refs/heads/main", SizeInBytes: 1 << 30},
		{Key: "bazel-root-abc", Ref: "refs/heads/gh-readonly-queue/main/pr-1", SizeInBytes: 2 << 30},
		{Key: "other-def", Ref: "refs/heads/main", SizeInBytes: 1 << 30},
	}
	st := summariseCaches(caches, 4<<30, 3)
	if len(st.Dupes) != 1 {
		t.Fatalf("dupes = %v, want 1", st.Dupes)
	}
	if math.Abs(st.StrandedGB-2) > 1e-9 {
		t.Fatalf("stranded = %v GB, want 2", st.StrandedGB)
	}
	if math.Abs(st.Families["bazel-root"]-3) > 1e-9 {
		t.Fatalf("family total = %v, want 3", st.Families["bazel-root"])
	}
}

func TestRenderEscapesMatrixSyntaxInJobNames(t *testing.T) {
	svg := stackedBars([]barRow{{Name: "bazel (<a & b>)", Queue: 1, Exec: 2, Runs: 3}})
	if strings.Contains(svg, "<a & b>") {
		t.Fatal("job name was not escaped")
	}
	if !strings.Contains(svg, "&amp;") {
		t.Fatal("expected an escaped ampersand")
	}
}

func TestChartsHandleNoData(t *testing.T) {
	if !strings.Contains(lineChart(nil), "no data") {
		t.Error("lineChart(nil) should say so")
	}
	if !strings.Contains(stackedBars(nil), "no data") {
		t.Error("stackedBars(nil) should say so")
	}
}

func TestDashboardHasNoExternalReferences(t *testing.T) {
	cache := healthyCache()
	cache.Families["fam"] = 1
	cache.Copies["fam"] = 1
	out := renderHTML("o/r", "bazel.yml", []Run{{Start: at(20, 0), Minutes: 5}},
		nil, nil, 0, cache, nil, 5, "now", false)
	for _, marker := range []string{"http://", "https://", "src="} {
		if strings.Contains(out, marker) {
			t.Errorf("dashboard must stay self-contained, found %q", marker)
		}
	}
}
