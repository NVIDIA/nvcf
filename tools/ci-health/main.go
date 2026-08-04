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

// Command ci-health answers "why is my build slow" for a GitHub Actions
// repository.
//
// A workflow's wall-clock time is not one number, it is a stack:
//
//	wall clock  =  queue wait  +  critical-path job  +  gate jobs
//
// Only the middle term is what people mean by "the build". A run that spends
// four minutes waiting for a runner looks identical, in the Actions UI, to a run
// that spends four minutes compiling. This tool separates them, finds which
// matrix row is the long pole, and reports cache pressure, which is the usual
// reason a job that used to be fast no longer is.
//
// Usage:
//
//	ci-health --dashboard            # visual report, opens in a browser
//	ci-health --why                  # same findings, as text
//	ci-health                        # cache and quota only
//	ci-health --durations            # duration percentiles by week
//	ci-health --merge-times          # PR open to merge latency
//	ci-health --all
//	ci-health --workflow release-tags.yml   # default: bazel.yml
//	ci-health --repo OWNER/NAME             # default: NVIDIA/nvcf
//
// Trends read every retained run of the workflow, which is cheap because the
// runs endpoint paginates 100 at a time. Per-job analysis costs one API call per
// run, so it is limited to the most recent --runs runs.
//
// Requires the gh CLI, authenticated.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type options struct {
	repo       string
	workflow   string
	dashboard  string
	why        bool
	durations  bool
	mergeTimes bool
	all        bool
	runs       int
	history    int
	weeks      int
	prs        int
	noOpen     bool
}

func parseFlags() *options {
	o := &options{}
	flag.StringVar(&o.repo, "repo", "NVIDIA/nvcf", "repository as OWNER/NAME")
	flag.StringVar(&o.workflow, "workflow", "bazel.yml", "workflow file name")
	flag.StringVar(&o.dashboard, "dashboard", "", "write a self-contained HTML dashboard to this path (default build-health.html when the flag is given without a value)")
	flag.BoolVar(&o.why, "why", false, "rank the causes of slowness as text")
	flag.BoolVar(&o.durations, "durations", false, "duration percentiles by week")
	flag.BoolVar(&o.mergeTimes, "merge-times", false, "PR open-to-merge latency")
	flag.BoolVar(&o.all, "all", false, "cache, durations and merge times")
	flag.IntVar(&o.runs, "runs", 60, "runs to pull per-job detail for")
	flag.IntVar(&o.history, "history", 1000, "runs to trend over")
	flag.IntVar(&o.weeks, "weeks", 12, "weeks of trend to print")
	flag.IntVar(&o.prs, "prs", 100, "merged PRs to sample")
	flag.BoolVar(&o.noOpen, "no-open", false, "do not launch a browser")

	// Allow a bare --dashboard with no value, which is the common case.
	for i, a := range os.Args {
		if a == "--dashboard" || a == "-dashboard" {
			if i+1 >= len(os.Args) || len(os.Args[i+1]) > 0 && os.Args[i+1][0] == '-' {
				os.Args = append(os.Args[:i+1:i+1], append([]string{"build-health.html"}, os.Args[i+1:]...)...)
			}
			break
		}
	}
	flag.Parse()
	return o
}

func run() error {
	o := parseFlags()

	workflow := o.workflow
	if filepath.Ext(workflow) != ".yml" && filepath.Ext(workflow) != ".yaml" {
		workflow += ".yml"
	}

	needJobs := o.dashboard != "" || o.why || o.all
	needRuns := needJobs || o.durations

	var (
		runsList  []Run
		jobs      []Job
		truncated bool
	)
	if needRuns {
		var err error
		runsList, err = fetchRuns(o.repo, workflow, o.history)
		if err != nil {
			return err
		}
		if len(runsList) == 0 {
			return fmt.Errorf("no successful runs found for workflow %q in %s", workflow, o.repo)
		}
		truncated = len(runsList) >= o.history
	}
	if needJobs {
		sample := runsList
		if len(sample) > o.runs {
			sample = sample[:o.runs]
		}
		jobs = fetchJobs(o.repo, sample, 8)
	}

	usage := struct {
		Size  int64 `json:"active_caches_size_in_bytes"`
		Count int   `json:"active_caches_count"`
	}{}
	if err := getJSON("actions/cache/usage", o.repo, &usage); err != nil {
		return err
	}
	caches, err := fetchCaches(o.repo)
	if err != nil {
		return err
	}
	cache := summariseCaches(caches, usage.Size, usage.Count)

	stats := analyseJobs(jobs)
	poles, poleRuns := longPoles(jobs)
	var causes []Cause
	var wall float64
	if needJobs {
		causes, wall = diagnose(runsList, stats, poles, poleRuns, cache)
	}

	if o.dashboard != "" {
		out, err := filepath.Abs(o.dashboard)
		if err != nil {
			return err
		}
		generated := time.Now().UTC().Format("2006-01-02 15:04 UTC")
		body := renderHTML(o.repo, workflow, runsList, stats, poles, poleRuns, cache, causes, wall, generated, truncated)
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", out)
		if !o.noOpen {
			openBrowser(out)
		}
		return nil
	}

	if o.why || o.all {
		printWhy(causes, wall, runsList)
	}
	if o.durations || o.all {
		printDurations(runsList, workflow, o.weeks, truncated)
	}
	if o.mergeTimes || o.all {
		lat, err := fetchMergeLatencies(o.repo, o.prs)
		if err != nil {
			return err
		}
		printMergeTimes(lat)
	}
	if !o.why && !o.durations && !o.mergeTimes {
		printCache(cache)
	} else if o.all {
		fmt.Println()
		printCache(cache)
	}
	return nil
}

func openBrowser(path string) {
	url := "file://" + path
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// Best effort: a headless environment has no browser, and that is not an
	// error worth failing the report over.
	_ = cmd.Start()
}

func printCache(c CacheState) {
	quota := c.TotalGB / quotaGB * 100
	fmt.Printf("cache: %.2f GB / %.0f GB across %d entries (%.0f%% of quota)\n", c.TotalGB, quotaGB, c.Count, quota)
	switch {
	case quota >= 90:
		fmt.Println("  AT QUOTA: new entries are evicting existing ones. Builds still pass, just colder.")
	case quota >= 75:
		fmt.Println("  approaching quota; expect evictions soon")
	}
	if len(c.Families) > 0 {
		type fr struct {
			n string
			s float64
		}
		var fams []fr
		for n, s := range c.Families {
			fams = append(fams, fr{n, s})
		}
		sort.Slice(fams, func(i, j int) bool {
			if fams[i].s == fams[j].s {
				return fams[i].n < fams[j].n
			}
			return fams[i].s > fams[j].s
		})
		fmt.Println("\nlargest families:")
		for i, f := range fams {
			if i >= 8 {
				break
			}
			fmt.Printf("  %6.2f GB  %4.1f%% of quota  x%-3d %s\n", f.s, f.s/quotaGB*100, c.Copies[f.n], f.n)
		}
	}
	if len(c.Dupes) > 0 {
		fmt.Printf("\nkeys stored under multiple refs: %d\n", len(c.Dupes))
		if c.StrandedGB > 0 {
			fmt.Printf("  %.2f GB of that is on merge-queue refs, which are unrestorable\n", c.StrandedGB)
		}
	}
}

func printWhy(causes []Cause, wall float64, runs []Run) {
	mins := make([]float64, 0, len(runs))
	for _, r := range runs {
		mins = append(mins, r.Minutes)
	}
	fmt.Printf("\nmedian run %.1f min, p90 %.1f min over %d successful runs\n\n", wall, pctl(mins, 0.9), len(runs))
	if len(causes) == 0 {
		fmt.Println("nothing is dominating the wall clock right now")
		return
	}
	for i, c := range causes {
		fmt.Printf("%d. %s\n   %s\n   -> %s\n\n", i+1, c.Title, c.Detail, c.Fix)
	}
}

func printDurations(runs []Run, workflow string, show int, truncated bool) {
	weeks := weekly(runs, truncated)
	if len(weeks) == 0 {
		fmt.Printf("\nworkflow %q: no successful runs in the retained window\n", workflow)
		return
	}
	if len(weeks) > show {
		weeks = weeks[len(weeks)-show:]
	}
	fmt.Printf("\nworkflow %q (successful runs only), by week:\n", workflow)
	for _, w := range weeks {
		fmt.Printf("  %s  runs=%-5d median=%6.1f min  p90=%6.1f min\n", w.Label, w.Count, w.P50, w.P90)
	}
	fmt.Println("  NOTE: a low median can mean rows were skipped by change detection,")
	fmt.Println("        not that builds got faster. Compare p90 and run counts too.")
}

func printMergeTimes(lat []PRLatency) {
	if len(lat) == 0 {
		fmt.Println("\nno merged PRs in the sampled window")
		return
	}
	hours := make([]float64, 0, len(lat))
	for _, l := range lat {
		hours = append(hours, l.Hours)
	}
	fmt.Printf("\nPR open -> merge, %d merged PRs sampled:\n", len(lat))
	fmt.Printf("  median %.1f h   p90 %.1f h\n", pctl(hours, 0.5), pctl(hours, 0.9))
	sort.Slice(lat, func(i, j int) bool { return lat[i].Hours > lat[j].Hours })
	fmt.Println("  slowest:")
	for i, l := range lat {
		if i >= 5 {
			break
		}
		fmt.Printf("    #%-6d %8.1f h\n", l.Number, l.Hours)
	}
}
