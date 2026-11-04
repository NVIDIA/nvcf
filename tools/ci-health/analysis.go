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
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	quotaGB         = 10.0
	maxCacheEntries = 2000
)

// gateHints name the jobs that gate or fan out the matrix rather than doing
// build work. They are held out so they do not distort the per-subtree picture,
// and so the required-checks job is never blamed as the critical path.
var gateHints = []string{"detect changed", "required checks", "collect", "summary"}

type JobStat struct {
	Queue   []float64
	Exec    []float64
	Runs    int
	Skipped int
	Hosted  int
}

type Week struct {
	Label string
	P50   float64
	P90   float64
	Count int
}

type Cause struct {
	Weight float64
	Title  string
	Detail string
	Fix    string
	Note   bool
}

type PRLatency struct {
	Number int
	Hours  float64
}

type CacheState struct {
	TotalGB    float64
	Count      int
	Families   map[string]float64
	Copies     map[string]int
	Dupes      map[string][]string
	StrandedGB float64
}

func gb(n int64) float64 { return float64(n) / (1024 * 1024 * 1024) }

// pctl is a linear-interpolated percentile. Plain index truncation is too
// coarse at p90: for ten ascending samples it just returns the maximum.
func pctl(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	k := float64(len(s)-1) * p
	lo, hi := math.Floor(k), math.Ceil(k)
	if lo == hi {
		return s[int(k)]
	}
	return s[int(lo)]*(hi-k) + s[int(hi)]*(k-lo)
}

func isGate(name string) bool {
	low := strings.ToLower(name)
	for _, h := range gateHints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

// isSkippedMatrix reports whether a job never really ran. A row skipped by
// change detection keeps its unexpanded matrix expression as its name, which is
// the only signal GitHub gives that the row was elided rather than executed.
func isSkippedMatrix(j Job) bool {
	return j.Conclusion == "skipped" || strings.Contains(j.Name, "${{")
}

func analyseJobs(jobs []Job) map[string]*JobStat {
	stats := map[string]*JobStat{}
	for _, j := range jobs {
		s, ok := stats[j.Name]
		if !ok {
			s = &JobStat{}
			stats[j.Name] = s
		}
		if isSkippedMatrix(j) {
			s.Skipped++
			continue
		}
		if j.CreatedAt == nil || j.StartedAt == nil || j.CompletedAt == nil {
			continue
		}
		s.Runs++
		s.Queue = append(s.Queue, math.Max(0, j.StartedAt.Sub(*j.CreatedAt).Minutes()))
		s.Exec = append(s.Exec, math.Max(0, j.CompletedAt.Sub(*j.StartedAt).Minutes()))
		if strings.HasPrefix(j.RunnerName, "GitHub Actions") {
			s.Hosted++
		}
	}
	return stats
}

// longPoles counts how often each job is the last to finish in its run. That
// job sets the wall clock: no amount of parallelism gets below it.
func longPoles(jobs []Job) (map[string]int, int) {
	type last struct {
		name string
		at   float64
	}
	byRun := map[int64]last{}
	for _, j := range jobs {
		if isSkippedMatrix(j) || isGate(j.Name) || j.CompletedAt == nil {
			continue
		}
		at := float64(j.CompletedAt.UnixNano())
		if cur, ok := byRun[j.RunID]; !ok || at > cur.at {
			byRun[j.RunID] = last{name: j.Name, at: at}
		}
	}
	tally := map[string]int{}
	for _, l := range byRun {
		tally[l.name]++
	}
	return tally, len(byRun)
}

func summariseCaches(caches []Cache, totalBytes int64, count int) CacheState {
	st := CacheState{
		TotalGB:  gb(totalBytes),
		Count:    count,
		Families: map[string]float64{},
		Copies:   map[string]int{},
		Dupes:    map[string][]string{},
	}
	refs := map[string][]string{}
	for _, c := range caches {
		fam := c.Key
		if i := strings.LastIndex(fam, "-"); i > 0 {
			fam = fam[:i]
		}
		if len(fam) > 40 {
			fam = fam[:40]
		}
		st.Families[fam] += gb(c.SizeInBytes)
		st.Copies[fam]++
		refs[c.Key] = append(refs[c.Key], c.Ref)
	}
	for k, rs := range refs {
		if len(rs) > 1 {
			st.Dupes[k] = rs
		}
	}
	// A key on a merge-queue ref is the least useful copy: the branch is deleted
	// when the queue drains, so the entry can never be restored, yet it still
	// counts against the quota until evicted.
	for _, c := range caches {
		if _, dup := st.Dupes[c.Key]; dup && strings.Contains(c.Ref, "gh-readonly-queue") {
			st.StrandedGB += gb(c.SizeInBytes)
		}
	}
	return st
}

// weekly buckets runs by ISO week, oldest first.
//
// When the history window was truncated we hold only the tail of the oldest
// week, so its median comes from an arbitrary slice and is not comparable to
// the rest. Drop it rather than plot a misleading point.
func weekly(runs []Run, truncated bool) []Week {
	buckets := map[string][]float64{}
	for _, r := range runs {
		y, w := r.Start.ISOWeek()
		buckets[fmt.Sprintf("%d-W%02d", y, w)] = append(buckets[fmt.Sprintf("%d-W%02d", y, w)], r.Minutes)
	}
	labels := make([]string, 0, len(buckets))
	for k := range buckets {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	if truncated && len(labels) > 1 {
		labels = labels[1:]
	}
	weeks := make([]Week, 0, len(labels))
	for _, l := range labels {
		v := buckets[l]
		weeks = append(weeks, Week{Label: l, P50: pctl(v, 0.5), P90: pctl(v, 0.9), Count: len(v)})
	}
	return weeks
}

// diagnose ranks the causes of slowness by measured contribution to wall clock.
func diagnose(runs []Run, stats map[string]*JobStat, poles map[string]int, poleRuns int, cache CacheState) ([]Cause, float64) {
	var causes []Cause
	mins := make([]float64, 0, len(runs))
	for _, r := range runs {
		mins = append(mins, r.Minutes)
	}
	wall := pctl(mins, 0.5)

	build := map[string]*JobStat{}
	var queues []float64
	for n, s := range stats {
		if isGate(n) || s.Runs == 0 {
			continue
		}
		build[n] = s
		queues = append(queues, s.Queue...)
	}

	if len(queues) > 0 && wall > 0 {
		q50, q90 := pctl(queues, 0.5), pctl(queues, 0.9)
		share := q50 / wall * 100
		if share >= 10 || q90 >= 2 {
			causes = append(causes, Cause{
				Weight: share,
				Title:  "Runner queue wait",
				Detail: fmt.Sprintf("Jobs wait a median %.1f min (p90 %.1f min) for a runner before executing, %.0f%% of the %.1f min median run.", q50, q90, share, wall),
				Fix:    "Add runner capacity or reduce concurrent matrix width.",
			})
		}
	}

	if poleRuns > 0 && len(poles) > 0 {
		var name string
		var hits int
		for n, c := range poles {
			// Ties break on name so the report is deterministic.
			if c > hits || (c == hits && n < name) {
				name, hits = n, c
			}
		}
		if s, ok := build[name]; ok && len(s.Exec) > 0 {
			e50 := pctl(s.Exec, 0.5)
			share := 0.0
			if wall > 0 {
				share = e50 / wall * 100
			}
			causes = append(causes, Cause{
				Weight: share,
				Title:  "Critical path: " + name,
				Detail: fmt.Sprintf("Finishes last in %d/%d runs (%.0f%%), median %.1f min. Every other job waits on it.", hits, poleRuns, float64(hits)/float64(poleRuns)*100, e50),
				Fix:    "Nothing below this job's runtime is achievable; split or cache it.",
			})
		}
	}

	if quota := cache.TotalGB / quotaGB * 100; quota >= 75 {
		fix := "Trim the largest cache family."
		if cache.StrandedGB > 0 {
			fix = fmt.Sprintf("%.2f GB sits on merge-queue refs that can never be restored.", cache.StrandedGB)
		}
		causes = append(causes, Cause{
			Weight: quota / 4,
			Title:  "Cache pressure",
			Detail: fmt.Sprintf("%.2f GB of %.0f GB used (%.0f%%). GitHub evicts least-recently-used entries at quota, so jobs silently rebuild from cold.", cache.TotalGB, quotaGB, quota),
			Fix:    fix,
		})
	}

	skipped, slots := 0, 0
	for _, s := range stats {
		skipped += s.Skipped
		slots += s.Skipped + s.Runs
	}
	if slots > 0 && float64(skipped)/float64(slots) > 0.3 {
		causes = append(causes, Cause{
			Title:  "Note: change detection is skipping rows",
			Detail: fmt.Sprintf("%d/%d matrix slots (%.0f%%) were skipped. Medians look fast because work was avoided, not accelerated.", skipped, slots, float64(skipped)/float64(slots)*100),
			Fix:    "Compare p90 and run counts, not the median alone.",
			Note:   true,
		})
	}

	sort.SliceStable(causes, func(i, j int) bool { return causes[i].Weight > causes[j].Weight })
	return causes, wall
}
