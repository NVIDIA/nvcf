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

package nvca

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// podReadsReportInterval is the periodic instance status interval of the rate runs, the default.
var podReadsReportInterval = nvcaconfig.AgentTimeConfig{}.Complete().PeriodicInstanceStatusInterval

// podReadsSpan is a span of a rate run, as offsets from its start.
type podReadsSpan struct{ from, to time.Duration }

func (s podReadsSpan) contains(at time.Duration) bool { return at >= s.from && at < s.to }

// podReadsRateRun is an hour of the instance status loop over one request of Running Pod instances
// that ICMS was last told are Running.
type podReadsRateRun struct {
	instances int
	// terminated is how many more instances the request lists whose Pods are gone and that ICMS was
	// last told are terminated.
	terminated int
	cacheReads bool
	heartbeats bool
	// every is the time between passes, podReadsPassInterval when zero.
	every time.Duration
	// icmsDown fails every post to ICMS.
	icmsDown podReadsSpan
	// apiDown fails every live Pod read. The informer cache keeps its Pods.
	apiDown podReadsSpan
	// paused runs no passes.
	paused podReadsSpan
}

// podReadsRatePass is what one pass of a rate run read live and posted.
type podReadsRatePass struct {
	at time.Duration
	// gets is the Pod GETs the API server received, failed ones included, by Pod name.
	gets map[string]int
	// posts is the updates posted to ICMS, accepted or not, and fromCache those of instances the
	// pass did not read live.
	posts, fromCache int
	// terminatedGets is the Pod GETs of the terminated instances.
	terminatedGets int
	// staleness is the longest any instance has gone without a successful live read, or since the
	// run started, at the end of the pass.
	staleness time.Duration
}

// runPodReadsRate runs the instance status loop for an hour as run describes, and returns what
// each pass did.
func runPodReadsRate(t *testing.T, run podReadsRateRun) []podReadsRatePass {
	t.Helper()
	// Refresh slots are aligned to the Unix epoch, so a fixed start makes every run the same.
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	passesPerInterval := int(podReadsReportInterval / podReadsPassInterval)
	pods := make([]*corev1.Pod, 0, run.instances)
	statuses := make([]nvcav2beta1.InstanceStatus, 0, run.instances)
	for i := range run.instances {
		name := fmt.Sprintf("%d-sr-5f0c2a7e-1b2d-4c3e-9f8a-0123456789ab", i)
		pods = append(pods, podReadsRunningPod(name, fmt.Sprintf("uid-%d", i), "100", "198.51.100.7"))
		// Instances last reported on different passes come due for a periodic report on different passes.
		reportedAt := metav1.NewTime(start.Add(time.Duration(i%passesPerInterval)*podReadsPassInterval - podReadsReportInterval))
		statuses = append(statuses, podReadsInstance(name, types.ICMSInstanceRunning, &reportedAt))
	}
	var terminated []string
	for i := range run.terminated {
		name := fmt.Sprintf("%d-sr-5f0c2a7e-1b2d-4c3e-9f8a-0123456789ab", run.instances+i)
		terminated = append(terminated, name)
		reportedAt := metav1.NewTime(start.Add(time.Duration(i%passesPerInterval)*podReadsPassInterval - podReadsReportInterval))
		statuses = append(statuses, podReadsInstance(name, types.ICMSInstanceTerminated, &reportedAt))
	}
	clients, client := podReadsAPIServer(pods...)
	var flags []*featureflag.FeatureFlag
	if run.cacheReads {
		flags = append(flags, featureflag.InstancePodCacheReads)
	}
	kb, bc := newPodReadsBackend(clients, podReadsCache(t, pods...), flags...)
	bc.enablePeriodicInstanceStatusUpdate = run.heartbeats
	bc.periodicInstanceStatusUpdateInterval = podReadsReportInterval
	clock := &podReadsClock{now: start}
	ctx := core.WithClock(newTestContext(), clock.Now)
	client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if run.apiDown.contains(clock.now.Sub(start)) {
			return true, nil, apierrors.NewServiceUnavailable("the API server is unavailable")
		}
		return false, nil, nil
	})
	req := podReadsRequest(statuses...)

	lastLive := map[string]time.Time{}
	for _, st := range statuses {
		lastLive[st.ID] = start
	}
	every := run.every
	if every == 0 {
		every = podReadsPassInterval
	}
	var passes []podReadsRatePass
	for at := time.Duration(0); at < time.Hour; at += every {
		if run.paused.contains(at) {
			continue
		}
		clock.now = start.Add(at)
		post := func(types.ICMSRequestUpdateInfo) error {
			if run.icmsDown.contains(at) {
				return errors.New("ICMS is unavailable")
			}
			return nil
		}
		updates := runPodReadsPassPosting(ctx, kb, req, post)
		pass := podReadsRatePass{at: at, gets: livePodGets(client), posts: len(updates)}
		client.ClearActions()
		for _, upd := range updates {
			if pass.gets[upd.InstanceID] == 0 {
				pass.fromCache++
			}
		}
		for _, name := range terminated {
			pass.terminatedGets += pass.gets[name]
		}
		if !run.apiDown.contains(at) {
			for name := range pass.gets {
				lastLive[name] = clock.now
			}
		}
		for _, last := range lastLive {
			pass.staleness = max(pass.staleness, clock.now.Sub(last))
		}
		passes = append(passes, pass)
	}
	return passes
}

// podReadsRates is what the passes of a rate run did in a span, per minute of the span.
type podReadsRates struct {
	gets, posts, fromCache, terminatedGets float64
	staleness                              time.Duration
	// read is the instances read live in the span.
	read map[string]bool
}

func podReadsRatesIn(passes []podReadsRatePass, span podReadsSpan) podReadsRates {
	minutes := (span.to - span.from).Minutes()
	r := podReadsRates{read: map[string]bool{}}
	for _, p := range passes {
		if !span.contains(p.at) {
			continue
		}
		for name, n := range p.gets {
			r.gets += float64(n) / minutes
			r.read[name] = true
		}
		r.posts += float64(p.posts) / minutes
		r.fromCache += float64(p.fromCache) / minutes
		r.terminatedGets += float64(p.terminatedGets) / minutes
		r.staleness = max(r.staleness, p.staleness)
	}
	return r
}

// The instance status loop reads Pods live no more often than its refresh window and the periodic
// reports call for: from an agent start, steadily, through an ICMS or API server outage, and after
// the loop pauses. Every update it posts is built from a live read, and while live reads succeed no
// instance goes a refresh window without one, except right after a pause with periodic reports
// disabled. Once the loop revisits instances less often than the refresh tracker's retention, with
// periodic reports disabled, it stops reading them live. An instance ICMS was told terminated, whose
// Pod is gone, is read live no more often than a running one. With cache reads disabled, every pass
// reads every instance live.
func TestInstancePodReads_LiveReadRates(t *testing.T) {
	// A refresh only bounds how long a stalled watch hides a change while it comes more often than the
	// periodic reports.
	require.Less(t, instancePodRefreshWindow, podReadsReportInterval)
	const instances = 50
	n := float64(instances)
	everyPass := n * float64(time.Minute/podReadsPassInterval)
	interval := podReadsReportInterval
	// liveReads is the most live reads per minute over a span of length d: per instance, one per
	// refresh window and, with periodic reports, one per report, and one more when the span starts
	// with every instance due, as after an outage or a pause.
	liveReads := func(d time.Duration, heartbeats, allDue bool) float64 {
		reads := math.Ceil(float64(d) / float64(instancePodRefreshWindow))
		if heartbeats {
			reads += math.Ceil(float64(d) / float64(interval))
		}
		if allDue {
			reads++
		}
		return n * reads / d.Minutes()
	}
	// heartbeatPosts is the fewest periodic reports per minute over d: one per instance and
	// interval, less one for an interval cut short at either end.
	heartbeatPosts := func(d time.Duration) float64 {
		return n * (float64(d/interval) - 1) / d.Minutes()
	}
	var (
		hour        = podReadsSpan{0, time.Hour}
		startup     = podReadsSpan{0, 5 * time.Minute}
		steady      = podReadsSpan{10 * time.Minute, time.Hour}
		outage      = podReadsSpan{20 * time.Minute, 30 * time.Minute}
		afterOutage = podReadsSpan{30 * time.Minute, 35 * time.Minute}
		pause       = podReadsSpan{20 * time.Minute, 40 * time.Minute}
		resume      = podReadsSpan{40 * time.Minute, 40*time.Minute + podReadsPassInterval}
		firstWindow = podReadsSpan{40 * time.Minute, 40*time.Minute + instancePodRefreshWindow + podReadsPassInterval}
		afterPause  = podReadsSpan{40 * time.Minute, 45 * time.Minute}
	)
	type window struct {
		name string
		span podReadsSpan
		// maxGets and minGets bound the live Pod GETs per minute, and minPosts the posts per minute.
		maxGets, minGets, minPosts float64
		// maxTerminatedGets bounds the live Pod GETs per minute of the terminated instances.
		maxTerminatedGets float64
		// readsAll requires every instance to be read live in the span.
		readsAll bool
		// stale is set when instances may go a refresh window without a successful live read.
		stale bool
	}
	tests := []struct {
		name    string
		run     podReadsRateRun
		windows []window
	}{
		{
			name: "cache reads disabled",
			run:  podReadsRateRun{heartbeats: true},
			windows: []window{
				{name: "hour", span: hour, maxGets: everyPass, minGets: everyPass, minPosts: heartbeatPosts(time.Hour)},
			},
		},
		{
			name: "periodic reports disabled",
			run:  podReadsRateRun{cacheReads: true},
			windows: []window{
				{name: "startup", span: startup, maxGets: liveReads(5*time.Minute, false, false)},
				{name: "steady", span: steady, maxGets: liveReads(50*time.Minute, false, false)},
				{name: "hour", span: hour, maxGets: liveReads(time.Hour, false, false)},
			},
		},
		{
			name: "periodic reports enabled",
			run:  podReadsRateRun{cacheReads: true, heartbeats: true},
			windows: []window{
				{name: "startup", span: startup, maxGets: liveReads(5*time.Minute, true, false)},
				{name: "steady", span: steady, maxGets: liveReads(50*time.Minute, true, false)},
				{name: "hour", span: hour, maxGets: liveReads(time.Hour, true, false), minPosts: heartbeatPosts(time.Hour)},
			},
		},
		{
			name: "ICMS outage",
			run:  podReadsRateRun{cacheReads: true, heartbeats: true, icmsDown: outage},
			windows: []window{
				{name: "outage", span: outage, maxGets: everyPass},
				{name: "after the outage", span: afterOutage, maxGets: liveReads(5*time.Minute, true, true)},
			},
		},
		{
			name: "API server outage",
			run:  podReadsRateRun{cacheReads: true, heartbeats: true, apiDown: outage},
			windows: []window{
				{name: "outage", span: outage, maxGets: everyPass, stale: true},
				{name: "after the outage", span: afterOutage, maxGets: liveReads(5*time.Minute, true, true)},
			},
		},
		{
			name: "API server outage with periodic reports disabled",
			run:  podReadsRateRun{cacheReads: true, apiDown: outage},
			windows: []window{
				{name: "outage", span: outage, maxGets: everyPass, stale: true},
				{name: "after the outage", span: afterOutage, maxGets: liveReads(5*time.Minute, false, true)},
			},
		},
		{
			name: "loop paused",
			run:  podReadsRateRun{cacheReads: true, heartbeats: true, paused: pause},
			windows: []window{
				{name: "first pass after the pause", span: resume, maxGets: everyPass, minGets: everyPass},
				{name: "after the pause", span: afterPause, maxGets: liveReads(5*time.Minute, true, true)},
			},
		},
		{
			// A terminated instance is read live, and reported, as a running one is.
			name: "terminated instances",
			run:  podReadsRateRun{cacheReads: true, heartbeats: true, terminated: instances},
			windows: []window{
				{
					name: "hour", span: hour,
					maxGets:           2 * liveReads(time.Hour, true, false),
					maxTerminatedGets: liveReads(time.Hour, true, false),
					minPosts:          2 * heartbeatPosts(time.Hour),
				},
			},
		},
		{
			name: "terminated instances with periodic reports disabled",
			run:  podReadsRateRun{cacheReads: true, terminated: instances},
			windows: []window{
				{
					name: "hour", span: hour,
					maxGets:           2 * liveReads(time.Hour, false, false),
					maxTerminatedGets: liveReads(time.Hour, false, false),
				},
			},
		},
		{
			name: "terminated instances with cache reads disabled",
			run:  podReadsRateRun{heartbeats: true, terminated: instances},
			windows: []window{
				{
					name: "hour", span: hour, maxGets: 2 * everyPass, minGets: 2 * everyPass,
					maxTerminatedGets: everyPass, minPosts: 2 * heartbeatPosts(time.Hour),
				},
			},
		},
		{
			// The tracker forgets the instances during the pause, so each starts a new refresh
			// window when the loop resumes.
			name: "loop paused with periodic reports disabled",
			run:  podReadsRateRun{cacheReads: true, paused: pause},
			windows: []window{
				// Each instance starts a new window, so the first pass reads none live.
				{name: "first pass after the pause", span: resume, maxGets: 0, stale: true},
				{name: "first window after the pause", span: firstWindow, maxGets: everyPass, readsAll: true, stale: true},
				{name: "after the pause", span: afterPause, maxGets: liveReads(5*time.Minute, false, false), stale: true},
			},
		},
		{
			// The tracker forgets each instance between visits, so no instance is ever refreshed.
			name: "loop revisits slower than the retention with periodic reports disabled",
			run:  podReadsRateRun{cacheReads: true, every: instancePodLiveReadRetention + time.Minute},
			windows: []window{
				{name: "hour", span: hour, maxGets: 0, stale: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run.instances = instances
			passes := runPodReadsRate(t, tt.run)
			for _, w := range tt.windows {
				r := podReadsRatesIn(passes, w.span)
				t.Logf("%s, %v to %v: %.2f live Pod GETs/min (bound %.2f), %.2f of terminated instances (bound %.2f), "+
					"%.2f posts/min, staleness up to %v",
					w.name, w.span.from, w.span.to, r.gets, w.maxGets, r.terminatedGets, w.maxTerminatedGets, r.posts, r.staleness)
				assert.LessOrEqual(t, r.gets, w.maxGets+1e-9, "%s: live Pod GETs per minute", w.name)
				assert.LessOrEqual(t, r.terminatedGets, w.maxTerminatedGets+1e-9,
					"%s: live Pod GETs per minute of terminated instances", w.name)
				assert.GreaterOrEqual(t, r.gets, w.minGets-1e-9, "%s: live Pod GETs per minute", w.name)
				assert.GreaterOrEqual(t, r.posts, w.minPosts-1e-9, "%s: posts per minute", w.name)
				assert.Zero(t, r.fromCache, "%s: posts not built from a live read", w.name)
				if w.readsAll {
					assert.Len(t, r.read, instances, "%s: instances read live", w.name)
				}
				if !w.stale {
					assert.Less(t, r.staleness, instancePodRefreshWindow, "%s: longest without a live read", w.name)
				}
			}
		})
	}
}
