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
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// fetch is the single point where this tool talks to GitHub. Tests replace it,
// which is why every other function here is exercised without a network.
var fetch = func(path, repo string) ([]byte, error) {
	out, err := exec.Command("gh", "api", "repos/"+repo+"/"+path).Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			return nil, fmt.Errorf("gh api %s: %s", path, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("gh api %s: %w", path, err)
	}
	return out, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func getJSON(path, repo string, v any) error {
	raw, err := fetch(path, repo)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// paged walks a list endpoint until limit items or the data runs out.
//
// key names the array field for endpoints that wrap their results in an object
// (actions/caches, actions/runs). Pass "" for endpoints that return a bare
// array, such as pulls; asking for a key there is a decode error, not an empty
// result.
func paged(path, repo, key string, limit int) ([]json.RawMessage, error) {
	var items []json.RawMessage
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; len(items) < limit; page++ {
		raw, err := fetch(fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), repo)
		if err != nil {
			return nil, err
		}
		var batch []json.RawMessage
		if key == "" {
			if err := json.Unmarshal(raw, &batch); err != nil {
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
		} else {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return nil, fmt.Errorf("decode %s: %w", path, err)
			}
			if body, ok := obj[key]; ok {
				if err := json.Unmarshal(body, &batch); err != nil {
					return nil, fmt.Errorf("decode %s.%s: %w", path, key, err)
				}
			}
		}
		if len(batch) == 0 {
			break
		}
		items = append(items, batch...)
		if len(batch) < 100 {
			break
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

type apiRun struct {
	ID           int64      `json:"id"`
	RunStartedAt *time.Time `json:"run_started_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
	HeadBranch   string     `json:"head_branch"`
}

// Job is one row of a workflow run. started_at and completed_at are absent for
// jobs that never ran, so they stay pointers.
type Job struct {
	Name        string     `json:"name"`
	RunID       int64      `json:"run_id"`
	CreatedAt   *time.Time `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Conclusion  string     `json:"conclusion"`
	RunnerName  string     `json:"runner_name"`
}

type Cache struct {
	Key         string `json:"key"`
	Ref         string `json:"ref"`
	SizeInBytes int64  `json:"size_in_bytes"`
}

type apiPR struct {
	Number    int        `json:"number"`
	CreatedAt *time.Time `json:"created_at"`
	MergedAt  *time.Time `json:"merged_at"`
}

// Run is a workflow run reduced to what the report needs.
type Run struct {
	ID      int64
	Start   time.Time
	Minutes float64
	Branch  string
}

func fetchRuns(repo, workflow string, limit int) ([]Run, error) {
	raws, err := paged("actions/workflows/"+workflow+"/runs?status=success", repo, "workflow_runs", limit)
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, raw := range raws {
		var r apiRun
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		if r.RunStartedAt == nil || r.UpdatedAt == nil || r.UpdatedAt.Before(*r.RunStartedAt) {
			continue
		}
		runs = append(runs, Run{
			ID:      r.ID,
			Start:   *r.RunStartedAt,
			Minutes: r.UpdatedAt.Sub(*r.RunStartedAt).Minutes(),
			Branch:  r.HeadBranch,
		})
	}
	return runs, nil
}

// fetchJobs reads jobs for each run concurrently. One call per run is the only
// way GitHub exposes job timings, so this is the expensive part of a report.
// A run whose jobs cannot be read is skipped rather than failing the report.
func fetchJobs(repo string, runs []Run, workers int) []Job {
	type result struct{ jobs []Job }
	sem := make(chan struct{}, workers)
	out := make([]result, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Paginate. A run with more than 100 jobs would otherwise report
			// partial timings, and the wide matrix rows are exactly the runs
			// whose numbers matter most.
			var all []Job
			for page := 1; ; page++ {
				var body struct {
					Total int   `json:"total_count"`
					Jobs  []Job `json:"jobs"`
				}
				path := fmt.Sprintf("actions/runs/%d/jobs?per_page=100&page=%d", id, page)
				if err := getJSON(path, repo, &body); err != nil {
					return
				}
				all = append(all, body.Jobs...)
				if len(body.Jobs) < 100 || len(all) >= body.Total {
					break
				}
			}
			out[i] = result{jobs: all}
		}(i, run.ID)
	}
	wg.Wait()
	var jobs []Job
	for _, r := range out {
		jobs = append(jobs, r.jobs...)
	}
	return jobs
}

func fetchCaches(repo string) ([]Cache, error) {
	raws, err := paged("actions/caches", repo, "actions_caches", maxCacheEntries)
	if err != nil {
		return nil, err
	}
	caches := make([]Cache, 0, len(raws))
	for _, raw := range raws {
		var c Cache
		if err := json.Unmarshal(raw, &c); err == nil {
			caches = append(caches, c)
		}
	}
	return caches, nil
}

func fetchMergeLatencies(repo string, limit int) ([]PRLatency, error) {
	// This endpoint returns a bare array, hence the empty key.
	raws, err := paged("pulls?state=closed&sort=updated&direction=desc", repo, "", limit)
	if err != nil {
		return nil, err
	}
	var out []PRLatency
	for _, raw := range raws {
		var p apiPR
		if err := json.Unmarshal(raw, &p); err != nil || p.MergedAt == nil || p.CreatedAt == nil {
			continue
		}
		out = append(out, PRLatency{Number: p.Number, Hours: p.MergedAt.Sub(*p.CreatedAt).Hours()})
	}
	return out, nil
}
