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
	"sort"
	"time"
)

// Stats summarizes a set of PodTimings (typically all pods of one
// function-version, or all pods overall). NotReady pods are excluded
// from the averages — including them would always pull the cold-start
// number towards zero during a scale-up while pods are still warming.
type Stats struct {
	// Total number of pods in this group, regardless of phase.
	Total int `json:"total"`

	// Counts by mode (over Ready pods only).
	Cold     int `json:"coldStart"`
	Restored int `json:"restored"`
	NotReady int `json:"notReady"`

	// Averages over Ready pods only. Zero when no Ready pods of that
	// mode exist. AvgCold and AvgRestored are durations (seconds).
	AvgCold     time.Duration `json:"avgColdStartSeconds"`
	AvgRestored time.Duration `json:"avgRestoredSeconds"`

	// AvgSavings is AvgCold - AvgRestored when both are positive,
	// zero otherwise. Multiplying by Restored gives total wall-clock
	// savings, which the table footer surfaces.
	AvgSavings   time.Duration `json:"avgSavingsSeconds"`
	TotalSavings time.Duration `json:"totalSavingsSeconds"`

	// SpeedupX is AvgCold / AvgRestored when both positive. The
	// "NvSnap is N× faster" headline number.
	SpeedupX float64 `json:"speedupX"`
}

// FunctionGroup is one (function-version, stats) bucket of
// SummaryByFunction's output.
type FunctionGroup struct {
	// FunctionVersionID is whatever value Hook A stamped on the
	// nvsnap.io/function-version-id annotation. May be empty if the
	// pod predates Hook A or wasn't admitted through NVCA.
	FunctionVersionID string `json:"functionVersionId"`

	// Stats over this fvID's pods.
	Stats Stats `json:"stats"`
}

// Summary computes overall Stats across all timings.
func Summary(timings []PodTiming) Stats {
	var s Stats
	s.Total = len(timings)
	var sumCold, sumRestored time.Duration
	for i := range timings {
		t := &timings[i]
		if t.NotReady {
			s.NotReady++
			continue
		}
		switch t.Mode {
		case ModeColdStart:
			s.Cold++
			sumCold += t.Duration
		case ModeRestored:
			s.Restored++
			sumRestored += t.Duration
		}
	}
	if s.Cold > 0 {
		s.AvgCold = (sumCold / time.Duration(s.Cold)).Round(time.Second)
	}
	if s.Restored > 0 {
		s.AvgRestored = (sumRestored / time.Duration(s.Restored)).Round(time.Second)
	}
	if s.AvgCold > 0 && s.AvgRestored > 0 && s.AvgCold > s.AvgRestored {
		s.AvgSavings = s.AvgCold - s.AvgRestored
		s.TotalSavings = s.AvgSavings * time.Duration(s.Restored)
		s.SpeedupX = float64(s.AvgCold) / float64(s.AvgRestored)
	}
	return s
}

// SummaryByFunction returns one FunctionGroup per distinct
// function-version-id, sorted by id. Empty fvIDs collapse into a
// single "unattributed" group with empty FunctionVersionID — that's
// the bucket pre-NVCA or non-NVCA pods land in.
func SummaryByFunction(timings []PodTiming) []FunctionGroup {
	groups := map[string][]PodTiming{}
	for i := range timings {
		t := &timings[i]
		groups[t.FunctionVersionID] = append(groups[t.FunctionVersionID], *t)
	}
	out := make([]FunctionGroup, 0, len(groups))
	for fvID, ts := range groups {
		out = append(out, FunctionGroup{
			FunctionVersionID: fvID,
			Stats:             Summary(ts),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].FunctionVersionID < out[j].FunctionVersionID
	})
	return out
}
