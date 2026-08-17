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
	"encoding/json"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/loadgen"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/render"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/sink"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
)

func TestDeploySinkCreatesResources(t *testing.T) {
	c := NewClientForClientset(fake.NewSimpleClientset())
	ctx := context.Background()

	dep, err := c.DeploySink(ctx, "byoo-perf", sink.DefaultOptions())
	if err != nil {
		t.Fatalf("DeploySink: %v", err)
	}
	if _, err := c.cs.CoreV1().ConfigMaps("byoo-perf").Get(ctx, sink.Name, metav1.GetOptions{}); err != nil {
		t.Errorf("sink configmap not created: %v", err)
	}
	if _, err := c.cs.CoreV1().Pods("byoo-perf").Get(ctx, dep.PodName, metav1.GetOptions{}); err != nil {
		t.Errorf("sink pod not created: %v", err)
	}
	if _, err := c.cs.CoreV1().Services("byoo-perf").Get(ctx, dep.ServiceName, metav1.GetOptions{}); err != nil {
		t.Errorf("sink service not created: %v", err)
	}
	if dep.HTTPEndpoint == "" || dep.GRPCEndpoint == "" {
		t.Errorf("sink endpoints not populated: %+v", dep)
	}
}

// stringDataToDataReactor mirrors the API server: on create it folds a Secret's
// StringData into Data (as raw bytes) and clears StringData, so tests observe
// the same shape a real cluster would return on read.
func stringDataToDataReactor(cs *fake.Clientset) {
	cs.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		if len(secret.StringData) > 0 {
			if secret.Data == nil {
				secret.Data = map[string][]byte{}
			}
			for k, v := range secret.StringData {
				secret.Data[k] = []byte(v)
			}
			secret.StringData = nil
		}
		return false, nil, nil
	})
}

func TestDeployWithExportCredentialsMountsSecret(t *testing.T) {
	cs := fake.NewSimpleClientset()
	stringDataToDataReactor(cs)
	c := NewClientForClientset(cs)
	ctx := context.Background()

	res, err := render.Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	creds := map[string]string{"perf-logs": "tok-a", "perf-metrics": "tok-b"}
	dep, err := c.Deploy(ctx, "byoo-perf", res, WithExportCredentials(creds))
	if err != nil {
		t.Fatalf("deploy with credentials: %v", err)
	}

	secretName := dep.PodName + "-export-creds"
	secret, err := c.cs.CoreV1().Secrets("byoo-perf").Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("credentials secret not created: %v", err)
	}
	// The secret carries a single accounts-secrets.json holding the name->token
	// map; the collector's extractor flattens it into per-signal token files.
	// The API server folds StringData into Data on write, so read from Data.
	raw, ok := secret.Data[accountsSecretsFile]
	if !ok {
		t.Fatalf("secret missing %q key: %+v", accountsSecretsFile, secret.Data)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("accounts-secrets.json is not valid JSON: %v", err)
	}
	for k, v := range creds {
		if got[k] != v {
			t.Errorf("accounts secret %q = %q, want %q", k, got[k], v)
		}
	}

	pod, err := c.cs.CoreV1().Pods("byoo-perf").Get(ctx, dep.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	// The volume mounted at the accounts-secrets input path must be backed by
	// the credentials Secret, not the emptyDir stand-in.
	var mountVol string
	for _, vm := range pod.Spec.Containers[0].VolumeMounts {
		if vm.MountPath == accountsSecretsMountPath {
			mountVol = vm.Name
		}
	}
	if mountVol == "" {
		t.Fatalf("collector does not mount %q", accountsSecretsMountPath)
	}
	found := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == mountVol {
			if v.Secret == nil || v.Secret.SecretName != secretName {
				t.Errorf("secrets volume not backed by credentials secret: %+v", v.VolumeSource)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("no volume named %q found", mountVol)
	}
}

// completeJobsReactor makes the fake clientset mark every created Job as
// complete, so RunLoad's wait returns without a real controller.
func completeJobsReactor(cs *fake.Clientset) {
	cs.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		})
		return false, nil, nil
	})
}

func TestRunLoadCreatesJobsAndWaits(t *testing.T) {
	cs := fake.NewSimpleClientset()
	completeJobsReactor(cs)
	c := NewClientForClientset(cs)
	ctx := context.Background()

	if err := c.EnsureNamespace(ctx, "byoo-perf"); err != nil {
		t.Fatalf("ensure ns: %v", err)
	}
	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
		Endpoint:      "collector.byoo-perf.svc.cluster.local:14357",
		Insecure:      true,
		Duration:      time.Second,
		LogsPerSec:    100,
		MetricsPerSec: 100,
	})
	if err := c.RunLoad(ctx, "byoo-perf", jobs, 5*time.Second); err != nil {
		t.Fatalf("RunLoad: %v", err)
	}
	created, err := cs.BatchV1().Jobs("byoo-perf").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(created.Items) != 2 {
		t.Errorf("expected 2 load jobs (logs+metrics), got %d", len(created.Items))
	}
}

func TestRunLoadFailsWhenJobFails(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type:    batchv1.JobFailed,
			Status:  corev1.ConditionTrue,
			Message: "boom",
		})
		return false, nil, nil
	})
	c := NewClientForClientset(cs)
	ctx := context.Background()
	if err := c.EnsureNamespace(ctx, "byoo-perf"); err != nil {
		t.Fatalf("ensure ns: %v", err)
	}
	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
		Endpoint: "x:14357", Duration: time.Second, LogsPerSec: 100,
	})
	if err := c.RunLoad(ctx, "byoo-perf", jobs, 5*time.Second); err == nil {
		t.Fatal("expected RunLoad to fail when a load job fails")
	}
}

func TestCleanupRemovesAllResourceKinds(t *testing.T) {
	cs := fake.NewSimpleClientset()
	completeJobsReactor(cs)
	c := NewClientForClientset(cs)
	ctx := context.Background()

	res, err := render.Render(spec.ShapeContainer, spec.DefaultOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := c.Deploy(ctx, "byoo-perf", res, WithExportCredentials(map[string]string{"perf-logs": "x"})); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := c.DeploySink(ctx, "byoo-perf", sink.DefaultOptions()); err != nil {
		t.Fatalf("deploy sink: %v", err)
	}
	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{Endpoint: "x:1", Duration: time.Second, LogsPerSec: 1})
	if err := c.RunLoad(ctx, "byoo-perf", jobs, 5*time.Second); err != nil {
		t.Fatalf("run load: %v", err)
	}

	// Unrelated resources without the suite label must survive.
	if _, err := cs.CoreV1().ConfigMaps("byoo-perf").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "keep-cm"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create unrelated cm: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("byoo-perf").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "keep-secret"}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create unrelated secret: %v", err)
	}

	if err := c.Cleanup(ctx, "byoo-perf"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	assertOnlyNamed := func(kind string, names []string, keep string) {
		if len(names) != 1 || names[0] != keep {
			t.Errorf("cleanup should leave only %q %s, got %v", keep, kind, names)
		}
	}

	cms, _ := cs.CoreV1().ConfigMaps("byoo-perf").List(ctx, metav1.ListOptions{})
	assertOnlyNamed("configmap", names(cms.Items, func(i int) string { return cms.Items[i].Name }), "keep-cm")

	secrets, _ := cs.CoreV1().Secrets("byoo-perf").List(ctx, metav1.ListOptions{})
	assertOnlyNamed("secret", names(secrets.Items, func(i int) string { return secrets.Items[i].Name }), "keep-secret")

	pods, _ := cs.CoreV1().Pods("byoo-perf").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Errorf("cleanup should remove all suite pods, got %d", len(pods.Items))
	}
	remJobs, _ := cs.BatchV1().Jobs("byoo-perf").List(ctx, metav1.ListOptions{})
	if len(remJobs.Items) != 0 {
		t.Errorf("cleanup should remove all suite jobs, got %d", len(remJobs.Items))
	}
}

// createJobsWithUID creates the given Jobs in the fake cluster, assigning each
// a stable UID so waitJobPodStarted can filter pods by owner. It returns a
// name->UID map for building owned pods.
func createJobsWithUID(t *testing.T, cs *fake.Clientset, ns string, jobs []*batchv1.Job) map[string]types.UID {
	t.Helper()
	uids := make(map[string]types.UID, len(jobs))
	for _, j := range jobs {
		j.UID = types.UID("uid-" + j.Name)
		uids[j.Name] = j.UID
		if _, err := cs.BatchV1().Jobs(ns).Create(context.Background(), j, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create job %q: %v", j.Name, err)
		}
	}
	return uids
}

func loadPod(namespace, jobName, jobLabelKey string, jobUID types.UID, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName + "-abc",
			Namespace:       namespace,
			Labels:          map[string]string{jobLabelKey: jobName},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: jobName, UID: jobUID}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// WaitLoadStarted must return once the generator pod reaches Running, even when
// the pod appears only after the Jobs are created (scheduling/image-pull delay).
// It must recognise the pod by either Job-controller label key.
func TestWaitLoadStartedWaitsForDelayedPod(t *testing.T) {
	for _, jobLabelKey := range []string{"job-name", "batch.kubernetes.io/job-name"} {
		t.Run(jobLabelKey, func(t *testing.T) {
			prev := loadStartPollInterval
			loadStartPollInterval = 10 * time.Millisecond
			defer func() { loadStartPollInterval = prev }()

			cs := fake.NewSimpleClientset()
			c := NewClientForClientset(cs)
			ctx := context.Background()

			jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
				Endpoint: "x:14357", Duration: time.Second, LogsPerSec: 100,
			})
			uids := createJobsWithUID(t, cs, "byoo-perf", jobs)
			// Pod becomes Running only after job creation.
			go func() {
				time.Sleep(50 * time.Millisecond)
				for _, j := range jobs {
					_, _ = cs.CoreV1().Pods("byoo-perf").Create(ctx, loadPod("byoo-perf", j.Name, jobLabelKey, uids[j.Name], corev1.PodRunning), metav1.CreateOptions{})
				}
			}()

			if err := c.WaitLoadStarted(ctx, "byoo-perf", jobs, 5*time.Second); err != nil {
				t.Fatalf("WaitLoadStarted: %v", err)
			}
		})
	}
}

// A Failed pod left over from a previous repetition (a different Job UID under
// the same name) must not abort the wait for the current run's pod.
func TestWaitLoadStartedIgnoresStalePodFromPreviousRun(t *testing.T) {
	prev := loadStartPollInterval
	loadStartPollInterval = 10 * time.Millisecond
	defer func() { loadStartPollInterval = prev }()

	cs := fake.NewSimpleClientset()
	c := NewClientForClientset(cs)
	ctx := context.Background()

	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
		Endpoint: "x:14357", Duration: time.Second, LogsPerSec: 100,
	})
	uids := createJobsWithUID(t, cs, "byoo-perf", jobs)
	for _, j := range jobs {
		// Stale Failed pod owned by an older Job UID under the same name.
		stale := loadPod("byoo-perf", j.Name, "job-name", types.UID("old-"+j.Name), corev1.PodFailed)
		stale.Name = j.Name + "-stale"
		if _, err := cs.CoreV1().Pods("byoo-perf").Create(ctx, stale, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create stale pod: %v", err)
		}
	}
	// The current run's pod comes up Running shortly after.
	go func() {
		time.Sleep(50 * time.Millisecond)
		for _, j := range jobs {
			_, _ = cs.CoreV1().Pods("byoo-perf").Create(ctx, loadPod("byoo-perf", j.Name, "job-name", uids[j.Name], corev1.PodRunning), metav1.CreateOptions{})
		}
	}()

	if err := c.WaitLoadStarted(ctx, "byoo-perf", jobs, 5*time.Second); err != nil {
		t.Fatalf("WaitLoadStarted aborted on a stale previous-run pod: %v", err)
	}
}

// A Failed pod owned by the current Job must still fail the wait.
func TestWaitLoadStartedFailsOnCurrentPodFailure(t *testing.T) {
	prev := loadStartPollInterval
	loadStartPollInterval = 10 * time.Millisecond
	defer func() { loadStartPollInterval = prev }()

	cs := fake.NewSimpleClientset()
	c := NewClientForClientset(cs)
	ctx := context.Background()

	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
		Endpoint: "x:14357", Duration: time.Second, LogsPerSec: 100,
	})
	uids := createJobsWithUID(t, cs, "byoo-perf", jobs)
	for _, j := range jobs {
		if _, err := cs.CoreV1().Pods("byoo-perf").Create(ctx, loadPod("byoo-perf", j.Name, "job-name", uids[j.Name], corev1.PodFailed), metav1.CreateOptions{}); err != nil {
			t.Fatalf("create failed pod: %v", err)
		}
	}
	if err := c.WaitLoadStarted(ctx, "byoo-perf", jobs, 2*time.Second); err == nil {
		t.Fatal("expected WaitLoadStarted to fail when the current Job's pod failed")
	}
}

// If no generator pod ever starts, WaitLoadStarted must return promptly on the
// bounded timeout instead of hanging the run.
func TestWaitLoadStartedTimesOut(t *testing.T) {
	prev := loadStartPollInterval
	loadStartPollInterval = 10 * time.Millisecond
	defer func() { loadStartPollInterval = prev }()

	cs := fake.NewSimpleClientset()
	c := NewClientForClientset(cs)
	jobs := loadgen.Jobs("byoo-perf", "perf-collector", loadgen.Options{
		Endpoint: "x:14357", Duration: time.Second, LogsPerSec: 100,
	})
	createJobsWithUID(t, cs, "byoo-perf", jobs)
	if err := c.WaitLoadStarted(context.Background(), "byoo-perf", jobs, 100*time.Millisecond); err == nil {
		t.Fatal("expected WaitLoadStarted to time out when no pod starts")
	}
}

func names[T any](items []T, get func(int) string) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = get(i)
	}
	return out
}
