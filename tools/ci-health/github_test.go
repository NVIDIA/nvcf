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
	"strings"
	"testing"
)

// stubFetch replaces the network layer with canned pages and records the paths
// requested, so pagination is asserted rather than assumed.
func stubFetch(t *testing.T, pages []string) *[]string {
	t.Helper()
	var calls []string
	prev := fetch
	fetch = func(path, repo string) ([]byte, error) {
		calls = append(calls, path)
		idx := 0
		if i := strings.Index(path, "&page="); i >= 0 {
			fmt.Sscanf(path[i+len("&page="):], "%d", &idx)
			idx--
		}
		if idx < len(pages) {
			return []byte(pages[idx]), nil
		}
		if strings.HasPrefix(strings.TrimSpace(pages[0]), "[") {
			return []byte("[]"), nil
		}
		return []byte("{}"), nil
	}
	t.Cleanup(func() { fetch = prev })
	return &calls
}

func arrayPage(n, from int) string {
	items := make([]string, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d}`, from+i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func wrappedPage(key string, n, from int) string {
	return fmt.Sprintf(`{%q:%s}`, key, arrayPage(n, from))
}

// The pulls endpoint returns a bare array. Asking for a wrapper key there used
// to crash the merge-time report once --prs exceeded one page.
func TestPagedBareArrayEndpoint(t *testing.T) {
	stubFetch(t, []string{arrayPage(100, 0), arrayPage(1, 100)})
	got, err := paged("pulls?state=closed", "o/r", "", 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 101 {
		t.Fatalf("got %d items, want 101", len(got))
	}
}

func TestPagedWrappedObjectEndpoint(t *testing.T) {
	stubFetch(t, []string{wrappedPage("workflow_runs", 100, 0), wrappedPage("workflow_runs", 1, 100)})
	got, err := paged("actions/workflows/x/runs", "o/r", "workflow_runs", 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 101 {
		t.Fatalf("got %d items, want 101", len(got))
	}
}

func TestPagedStopsAtLimit(t *testing.T) {
	calls := stubFetch(t, []string{arrayPage(100, 0), arrayPage(100, 100), arrayPage(100, 200)})
	got, err := paged("pulls", "o/r", "", 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 {
		t.Fatalf("got %d items, want 150", len(got))
	}
	if len(*calls) != 2 {
		t.Fatalf("made %d calls, want 2; must not page past the limit", len(*calls))
	}
}

func TestPagedStopsOnShortPage(t *testing.T) {
	calls := stubFetch(t, []string{arrayPage(1, 0)})
	got, err := paged("pulls", "o/r", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if len(*calls) != 1 {
		t.Fatalf("made %d calls, want 1; a short page means the data ran out", len(*calls))
	}
}

func TestPagedAppendsToExistingQuery(t *testing.T) {
	calls := stubFetch(t, []string{arrayPage(1, 0)})
	if _, err := paged("pulls?state=closed", "o/r", "", 10); err != nil {
		t.Fatal(err)
	}
	if want := "pulls?state=closed&per_page=100&page=1"; (*calls)[0] != want {
		t.Fatalf("path = %q, want %q", (*calls)[0], want)
	}
}

func TestPagedStartsQueryWhenPathHasNone(t *testing.T) {
	calls := stubFetch(t, []string{arrayPage(1, 0)})
	if _, err := paged("actions/caches", "o/r", "", 10); err != nil {
		t.Fatal(err)
	}
	if want := "actions/caches?per_page=100&page=1"; (*calls)[0] != want {
		t.Fatalf("path = %q, want %q", (*calls)[0], want)
	}
}

func TestPagedMissingKeyYieldsNothing(t *testing.T) {
	stubFetch(t, []string{`{"something_else":[{"number":1}]}`})
	got, err := paged("actions/caches", "o/r", "actions_caches", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d items, want 0", len(got))
	}
}

func TestPagedSurfacesDecodeErrors(t *testing.T) {
	stubFetch(t, []string{`not json`})
	if _, err := paged("pulls", "o/r", "", 10); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestFetchRunsSkipsRunsWithoutTimestamps(t *testing.T) {
	page := `{"workflow_runs":[
      {"id":1,"run_started_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:06:00Z","head_branch":"main"},
      {"id":2,"run_started_at":null,"updated_at":"2026-07-20T10:06:00Z"},
      {"id":3,"run_started_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T09:00:00Z"}
    ]}`
	stubFetch(t, []string{page})
	runs, err := fetchRuns("o/r", "bazel.yml", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1 (null and end-before-start must be dropped)", len(runs))
	}
	if runs[0].Minutes != 6 {
		t.Fatalf("minutes = %v, want 6", runs[0].Minutes)
	}
}

func TestFetchMergeLatenciesIgnoresUnmergedPRs(t *testing.T) {
	page := `[
      {"number":1,"created_at":"2026-07-20T00:00:00Z","merged_at":"2026-07-20T05:00:00Z"},
      {"number":2,"created_at":"2026-07-20T00:00:00Z","merged_at":null}
    ]`
	stubFetch(t, []string{page})
	lat, err := fetchMergeLatencies("o/r", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(lat) != 1 || lat[0].Number != 1 || lat[0].Hours != 5 {
		t.Fatalf("latencies = %+v", lat)
	}
}

func TestFetchJobsToleratesFailures(t *testing.T) {
	prev := fetch
	fetch = func(path, repo string) ([]byte, error) {
		if strings.Contains(path, "/2/") {
			return nil, fmt.Errorf("boom")
		}
		return []byte(`{"jobs":[{"name":"bazel (a)","run_id":1,"conclusion":"success"}]}`), nil
	}
	t.Cleanup(func() { fetch = prev })

	jobs := fetchJobs("o/r", []Run{{ID: 1}, {ID: 2}, {ID: 3}}, 2)
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2; a failed run must be skipped, not fatal", len(jobs))
	}
}

func TestJobDecodesNullTimestamps(t *testing.T) {
	var j Job
	body := `{"name":"x","run_id":1,"created_at":"2026-07-20T10:00:00Z","started_at":null,"completed_at":null,"conclusion":"skipped","runner_name":null}`
	if err := json.Unmarshal([]byte(body), &j); err != nil {
		t.Fatal(err)
	}
	if j.StartedAt != nil || j.CompletedAt != nil {
		t.Fatal("null timestamps must decode to nil, not the zero time")
	}
	if j.CreatedAt == nil {
		t.Fatal("created_at should have decoded")
	}
}
