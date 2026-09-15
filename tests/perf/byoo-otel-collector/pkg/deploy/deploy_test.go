/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/render"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
)

func TestEnsureNamespaceIdempotent(t *testing.T) {
	c := NewClientForClientset(fake.NewSimpleClientset())
	ctx := context.Background()

	if err := c.EnsureNamespace(ctx, "byoo-perf"); err != nil {
		t.Fatalf("first EnsureNamespace: %v", err)
	}
	if err := c.EnsureNamespace(ctx, "byoo-perf"); err != nil {
		t.Fatalf("second EnsureNamespace (should be idempotent): %v", err)
	}

	ns, err := c.cs.CoreV1().Namespaces().Get(ctx, "byoo-perf", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Labels[partOfLabelKey] != partOfLabelValue {
		t.Errorf("namespace missing part-of label: %v", ns.Labels)
	}
}

func TestDeployCreatesPodAndService(t *testing.T) {
	c := NewClientForClientset(fake.NewSimpleClientset())
	ctx := context.Background()

	res, err := render.Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	dep, err := c.Deploy(ctx, "byoo-perf", res)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	pod, err := c.cs.CoreV1().Pods("byoo-perf").Get(ctx, dep.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Labels[partOfLabelKey] != partOfLabelValue {
		t.Errorf("pod missing part-of label: %v", pod.Labels)
	}
	if pod.Labels[instanceLabelKey] != dep.PodName {
		t.Errorf("pod instance label = %q, want %q", pod.Labels[instanceLabelKey], dep.PodName)
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != render.CollectorContainerName {
		t.Fatalf("expected a single collector container, got %+v", pod.Spec.Containers)
	}

	svc, err := c.cs.CoreV1().Services("byoo-perf").Get(ctx, dep.ServiceName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.Spec.Selector[instanceLabelKey] != dep.PodName {
		t.Errorf("service selector = %v, want instance %q", svc.Spec.Selector, dep.PodName)
	}
	if len(svc.Spec.Ports) != len(pod.Spec.Containers[0].Ports) {
		t.Errorf("service exposes %d ports, want %d (one per collector port)", len(svc.Spec.Ports), len(pod.Spec.Containers[0].Ports))
	}
	if _, ok := dep.Endpoints["otlp-grpc"]; !ok {
		t.Errorf("expected an otlp-grpc endpoint, got %v", dep.Endpoints)
	}
}

func TestDeployIsRepeatable(t *testing.T) {
	c := NewClientForClientset(fake.NewSimpleClientset())
	ctx := context.Background()

	res, err := render.Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := c.Deploy(ctx, "byoo-perf", res); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if _, err := c.Deploy(ctx, "byoo-perf", res); err != nil {
		t.Fatalf("second deploy (should replace cleanly): %v", err)
	}

	pods, err := c.cs.CoreV1().Pods("byoo-perf").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Errorf("expected exactly 1 pod after repeated deploy, got %d", len(pods.Items))
	}
}

func TestWaitPodReadySucceeds(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	c := NewClientForClientset(fake.NewSimpleClientset(pod))

	if err := c.WaitPodReady(context.Background(), "byoo-perf", "perf-collector", 5*time.Second); err != nil {
		t.Fatalf("WaitPodReady on a ready pod: %v", err)
	}
}

func TestWaitPodReadyFailsFastOnCrashLoop(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: render.CollectorContainerName,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CrashLoopBackOff",
					Message: "back-off restarting failed container",
				}},
			}},
		},
	}
	c := NewClientForClientset(fake.NewSimpleClientset(pod))

	err := c.WaitPodReady(context.Background(), "byoo-perf", "perf-collector", 5*time.Second)
	if err == nil {
		t.Fatal("expected WaitPodReady to fail fast on CrashLoopBackOff")
	}
}

func TestWaitCollectorHealthRecordsStartTimes(t *testing.T) {
	origPoll, origProxy := healthPollInterval, proxyGet
	t.Cleanup(func() {
		healthPollInterval = origPoll
		proxyGet = origProxy
	})
	healthPollInterval = time.Millisecond

	podStartedAt := time.Now().Add(-8 * time.Second).UTC()
	collectorStartedAt := podStartedAt.Add(2 * time.Second)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: podStartedAt},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "byoo-otel-collector",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
					StartedAt: metav1.Time{Time: collectorStartedAt},
				}},
			}},
		},
	}
	var requests int
	proxyGet = func(_ context.Context, _ kubernetes.Interface, namespace, name, port, path string) ([]byte, error) {
		requests++
		if namespace != "byoo-perf" || name != "perf-collector" || port != collectorHealthPort || path != collectorHealthPath {
			t.Fatalf("health request = %s/%s:%s%s", namespace, name, port, path)
		}
		if requests == 1 {
			return nil, fmt.Errorf("collector is starting")
		}
		return []byte("ok"), nil
	}

	c := NewClientForClientset(fake.NewSimpleClientset(pod))
	before := time.Now()
	startup, err := c.WaitCollectorHealth(context.Background(), "byoo-perf", "perf-collector", "byoo-otel-collector", time.Second, time.Minute)
	if err != nil {
		t.Fatalf("WaitCollectorHealth: %v", err)
	}
	if requests != 2 {
		t.Errorf("health requests = %d, want 2", requests)
	}
	if !startup.PodStartedAt.Equal(podStartedAt) || !startup.CollectorStartedAt.Equal(collectorStartedAt) {
		t.Errorf("startup timestamps = %+v, want pod=%s collector=%s", startup, podStartedAt, collectorStartedAt)
	}
	if startup.HealthyAt.Before(before) {
		t.Errorf("healthy at %s before wait began %s", startup.HealthyAt, before)
	}
	if startup.PodToHealthSeconds < 8 || startup.CollectorToHealthSeconds < 6 {
		t.Errorf("startup durations = %+v, want at least pod=8s collector=6s", startup)
	}
}

func TestWaitCollectorHealthTimesOutWithoutContainerStart(t *testing.T) {
	origPoll := healthPollInterval
	t.Cleanup(func() { healthPollInterval = origPoll })
	healthPollInterval = time.Millisecond

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodPending,
			StartTime: &metav1.Time{Time: time.Now()},
		},
	}
	c := NewClientForClientset(fake.NewSimpleClientset(pod))
	_, err := c.WaitCollectorHealth(context.Background(), "byoo-perf", "perf-collector", "byoo-otel-collector", 10*time.Millisecond, time.Second)
	if err == nil {
		t.Fatal("expected WaitCollectorHealth to time out without a collector start time")
	}
	if !strings.Contains(err.Error(), "collector health endpoint") {
		t.Errorf("timeout error = %v, want collector health endpoint context", err)
	}
}

func TestWaitCollectorHealthStopsAtStartupMaximum(t *testing.T) {
	origPoll, origProxy := healthPollInterval, proxyGet
	t.Cleanup(func() {
		healthPollInterval = origPoll
		proxyGet = origProxy
	})
	healthPollInterval = time.Millisecond

	collectorStartedAt := time.Now().UTC()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: collectorStartedAt.Add(-time.Second)},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "byoo-otel-collector",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
					StartedAt: metav1.Time{Time: collectorStartedAt},
				}},
			}},
		},
	}
	proxyGet = func(context.Context, kubernetes.Interface, string, string, string, string) ([]byte, error) {
		return nil, errors.New("collector is still starting")
	}

	c := NewClientForClientset(fake.NewSimpleClientset(pod))
	startedWaiting := time.Now()
	_, err := c.WaitCollectorHealth(context.Background(), "byoo-perf", "perf-collector", "byoo-otel-collector", 250*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected WaitCollectorHealth to stop at the startup maximum")
	}
	if !strings.Contains(err.Error(), "startup maximum") {
		t.Errorf("error = %v, want startup maximum context", err)
	}
	if elapsed := time.Since(startedWaiting); elapsed > 100*time.Millisecond {
		t.Errorf("WaitCollectorHealth returned after %s, want it to stop near the 10ms startup maximum", elapsed)
	}
}

func TestWaitCollectorHealthRejectsLateHealthResponse(t *testing.T) {
	origPoll, origProxy := healthPollInterval, proxyGet
	t.Cleanup(func() {
		healthPollInterval = origPoll
		proxyGet = origProxy
	})
	healthPollInterval = time.Millisecond

	const startupMax = 100 * time.Millisecond
	collectorStartedAt := time.Now().UTC()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "perf-collector", Namespace: "byoo-perf"},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: collectorStartedAt.Add(-time.Second)},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "byoo-otel-collector",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
					StartedAt: metav1.Time{Time: collectorStartedAt},
				}},
			}},
		},
	}
	called := false
	proxyGet = func(ctx context.Context, _ kubernetes.Interface, _, _, _, _ string) ([]byte, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("health request has no startup deadline")
		} else if want := collectorStartedAt.Add(startupMax); !deadline.Equal(want) {
			t.Errorf("health request deadline = %s, want %s", deadline, want)
		}
		if delay := time.Until(deadline) + time.Millisecond; delay > 0 {
			time.Sleep(delay)
		}
		return []byte("ok"), nil
	}

	c := NewClientForClientset(fake.NewSimpleClientset(pod))
	_, err := c.WaitCollectorHealth(context.Background(), "byoo-perf", "perf-collector", "byoo-otel-collector", time.Second, startupMax)
	if !called {
		t.Fatal("expected a health request before the startup deadline")
	}
	if err == nil {
		t.Fatal("expected a late successful health response to fail")
	}
	if !strings.Contains(err.Error(), "startup maximum") {
		t.Errorf("error = %v, want startup maximum context", err)
	}
}

func TestCleanupScopedToLabel(t *testing.T) {
	c := NewClientForClientset(fake.NewSimpleClientset())
	ctx := context.Background()

	res, err := render.Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := c.Deploy(ctx, "byoo-perf", res); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// An unrelated pod without the suite's label must survive cleanup.
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "byoo-perf"}}
	if _, err := c.cs.CoreV1().Pods("byoo-perf").Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create unrelated pod: %v", err)
	}

	if err := c.Cleanup(ctx, "byoo-perf"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	pods, err := c.cs.CoreV1().Pods("byoo-perf").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Name != "unrelated" {
		t.Errorf("cleanup should leave only the unrelated pod, got %+v", pods.Items)
	}

	svcs, err := c.cs.CoreV1().Services("byoo-perf").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	if len(svcs.Items) != 0 {
		t.Errorf("cleanup should remove suite services, got %+v", svcs.Items)
	}
}

// An unresponsive pod or API proxy must not hang the run: ScrapePodMetrics
// bounds each fetch with scrapeTimeout and returns a deadline error the caller
// treats as a missing sample.
func TestScrapePodMetricsTimesOut(t *testing.T) {
	origGet, origTimeout := proxyGet, scrapeTimeout
	defer func() { proxyGet, scrapeTimeout = origGet, origTimeout }()

	scrapeTimeout = 20 * time.Millisecond
	proxyGet = func(ctx context.Context, _ kubernetes.Interface, _, _, _, _ string) ([]byte, error) {
		<-ctx.Done() // block like an unresponsive endpoint until the timeout fires
		return nil, ctx.Err()
	}

	c := NewClientForClientset(fake.NewSimpleClientset())
	start := time.Now()
	_, err := c.ScrapePodMetrics(context.Background(), "byoo-perf", "collector", "8888", "/metrics")
	if err == nil {
		t.Fatal("expected a timeout error from an unresponsive scrape")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("scrape ignored the timeout, took %v", elapsed)
	}
}
