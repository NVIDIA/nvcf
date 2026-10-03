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

package operator

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	validatorNS      = "nvca-operator"
	validatorCronJob = "nvca-operator-cluster-validator"
	validatorCronUID = types.UID("cj-uid")
)

func validatorCronJobWithSpec(spec string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: validatorCronJob, Namespace: validatorNS, UID: validatorCronUID},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 */3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app.kubernetes.io/component": "validation"},
					Annotations: map[string]string{validatorSpecAnnotation: spec},
				},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "cluster-validator", Image: "validator:" + spec}},
				}}},
			},
		},
	}
}

// pastValidatorJob is a finished Job the CronJob owns, created at age ago.
func pastValidatorJob(name, spec string, age time.Duration) *batchv1.Job {
	controller := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: validatorNS, UID: types.UID("uid-" + name),
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			Labels:            map[string]string{"app.kubernetes.io/component": "validation"},
			Annotations:       map[string]string{validatorSpecAnnotation: spec},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: validatorCronJob, UID: validatorCronUID,
				Controller: &controller,
			}},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}},
	}
}

// validatorClient dates the Jobs it creates, as the API server does, and
// counts the creates.
func validatorClient(objs ...runtime.Object) (*fake.Clientset, *atomic.Int32) {
	client := fake.NewSimpleClientset(objs...)
	creates := &atomic.Int32{}
	client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
		creates.Add(1)
		a.(ktesting.CreateAction).GetObject().(*batchv1.Job).CreationTimestamp = metav1.Now()
		return false, nil, nil
	})
	return client, creates
}

func newValidatorWatcher(client *fake.Clientset) *validatorRunWatcher {
	return &validatorRunWatcher{
		client: client, namespace: validatorNS, name: validatorCronJob, log: logrus.NewEntry(logrus.New()),
	}
}

func validatorJobs(t *testing.T, client *fake.Clientset) []batchv1.Job {
	t.Helper()
	jobs, err := client.BatchV1().Jobs(validatorNS).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	return jobs.Items
}

func setCronJob(t *testing.T, client *fake.Clientset, cj *batchv1.CronJob) {
	t.Helper()
	_, err := client.BatchV1().CronJobs(validatorNS).Update(context.Background(), cj, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func countVerb(client *fake.Clientset, verb, resource string) int {
	n := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == verb && a.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

// A new spec starts exactly one run, built from the CronJob's template and
// controlled by the CronJob. Polling again, or a restarted operator, starts
// none and leaves the run alone.
func TestValidatorRun_OncePerSpec(t *testing.T) {
	client, creates := validatorClient(validatorCronJobWithSpec("a"))
	ctx := context.Background()
	w := newValidatorWatcher(client)

	require.False(t, w.reconcile(ctx))
	jobs := validatorJobs(t, client)
	require.Len(t, jobs, 1)
	job := jobs[0]
	assert.Equal(t, "validator:a", job.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "validation", job.Labels["app.kubernetes.io/component"])
	assert.Equal(t, "manual", job.Annotations["cronjob.kubernetes.io/instantiate"])
	assert.Equal(t, "a", job.Annotations[validatorSpecAnnotation])
	controller := true
	assert.Equal(t, []metav1.OwnerReference{{
		APIVersion: "batch/v1", Kind: "CronJob", Name: validatorCronJob, UID: validatorCronUID, Controller: &controller,
	}}, job.OwnerReferences)
	assert.LessOrEqual(t, len(job.Name), 63)

	require.False(t, w.reconcile(ctx))
	require.False(t, newValidatorWatcher(client).reconcile(ctx), "an operator restart")
	assert.Equal(t, int32(1), creates.Load())
	assert.Zero(t, countVerb(client, "delete", "jobs"))
	after := validatorJobs(t, client)
	require.Len(t, after, 1)
	assert.Equal(t, job.UID, after[0].UID)
	assert.Equal(t, job.ResourceVersion, after[0].ResourceVersion)
}

// What decides is the spec the newest run used. A rollback to an earlier spec
// runs again, even though a run of that spec is still in the history.
func TestValidatorRun_RollbackRunsAgain(t *testing.T) {
	client, creates := validatorClient(validatorCronJobWithSpec("a"),
		pastValidatorJob("run-a", "a", 2*time.Hour), pastValidatorJob("run-b", "b", time.Hour))
	require.False(t, newValidatorWatcher(client).reconcile(context.Background()))
	assert.Equal(t, int32(1), creates.Load())
	assert.Len(t, validatorJobs(t, client), 3)
}

// A scheduled run of the current spec counts, so once the history limits
// prune the run the operator started, a restart still starts nothing.
func TestValidatorRun_ScheduledRunOfTheSpecCounts(t *testing.T) {
	client, creates := validatorClient(validatorCronJobWithSpec("b"),
		pastValidatorJob("scheduled-1", "a", 7*time.Hour), pastValidatorJob("scheduled-2", "b", time.Hour))
	require.False(t, newValidatorWatcher(client).reconcile(context.Background()))
	assert.Zero(t, creates.Load())
}

// Only Jobs the CronJob controls are history: another CronJob's run of the
// same spec, or a stray Job, does not count.
func TestValidatorRun_OnlyOwnedJobsCount(t *testing.T) {
	foreign := pastValidatorJob("foreign", "b", time.Minute)
	foreign.OwnerReferences[0].UID = "other-uid"
	stray := pastValidatorJob("stray", "b", time.Minute)
	stray.OwnerReferences = nil
	client, creates := validatorClient(validatorCronJobWithSpec("b"),
		pastValidatorJob("old", "a", time.Hour), foreign, stray)
	require.False(t, newValidatorWatcher(client).reconcile(context.Background()))
	assert.Equal(t, int32(1), creates.Load())
}

// At most one run at a time, and none while the CronJob is suspended. Each
// wait ends with a run once it clears.
func TestValidatorRun_RespectsSuspendAndRunsInProgress(t *testing.T) {
	ctx := context.Background()
	suspended := validatorCronJobWithSpec("b")
	suspend := true
	suspended.Spec.Suspend = &suspend
	active := validatorCronJobWithSpec("b")
	active.Status.Active = []corev1.ObjectReference{{Name: "scheduled"}}
	running := pastValidatorJob("running", "a", time.Minute)
	running.Status.Conditions = nil

	for name, tc := range map[string]struct {
		cronJob *batchv1.CronJob
		objs    []runtime.Object
		clear   func(*testing.T, *fake.Clientset)
	}{
		"suspended": {cronJob: suspended, clear: func(t *testing.T, c *fake.Clientset) {
			setCronJob(t, c, validatorCronJobWithSpec("b"))
		}},
		"scheduled run active": {cronJob: active, clear: func(t *testing.T, c *fake.Clientset) {
			setCronJob(t, c, validatorCronJobWithSpec("b"))
		}},
		"operator-started run unfinished": {cronJob: validatorCronJobWithSpec("b"), objs: []runtime.Object{running},
			clear: func(t *testing.T, c *fake.Clientset) {
				done := pastValidatorJob("running", "a", time.Minute)
				_, err := c.BatchV1().Jobs(validatorNS).Update(ctx, done, metav1.UpdateOptions{})
				require.NoError(t, err)
			}},
	} {
		t.Run(name, func(t *testing.T) {
			client, creates := validatorClient(append(tc.objs, tc.cronJob)...)
			w := newValidatorWatcher(client)
			require.False(t, w.reconcile(ctx))
			require.False(t, w.reconcile(ctx))
			assert.Zero(t, creates.Load())

			tc.clear(t, client)
			require.False(t, w.reconcile(ctx))
			assert.Equal(t, int32(1), creates.Load())
		})
	}
}

// A Job of the run's name that the CronJob does not own is neither replaced
// nor adopted, and the watcher does not keep retrying it.
func TestValidatorRun_ForeignJobOfTheSameNameIsLeftAlone(t *testing.T) {
	cronJob := validatorCronJobWithSpec("a")
	squatter := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: validatorRunJobName(validatorCronJob, "a", ""), Namespace: validatorNS, UID: "squatter",
	}}
	client, creates := validatorClient(cronJob, squatter)
	w := newValidatorWatcher(client)
	require.False(t, w.reconcile(context.Background()))
	require.False(t, w.reconcile(context.Background()))
	assert.Equal(t, int32(1), creates.Load())
	assert.Zero(t, countVerb(client, "delete", "jobs")+countVerb(client, "update", "jobs"))
	jobs := validatorJobs(t, client)
	require.Len(t, jobs, 1)
	assert.Empty(t, jobs[0].OwnerReferences)
}

// A CronJob without the spec annotation gives no way to tell what a
// scheduled run used, so nothing is started.
func TestValidatorRun_NoSpecAnnotationStartsNothing(t *testing.T) {
	cronJob := validatorCronJobWithSpec("a")
	cronJob.Spec.JobTemplate.Annotations = nil
	client, creates := validatorClient(cronJob)
	require.False(t, newValidatorWatcher(client).reconcile(context.Background()))
	assert.Zero(t, creates.Load())
}

func fastValidatorPolls(t *testing.T) {
	prev := validatorRunPollInterval
	validatorRunPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { validatorRunPollInterval = prev })
}

// startWatch runs the watch until the test ends, and waits for it to return
// before the test's earlier cleanups run.
func startWatch(t *testing.T, client *fake.Clientset) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchValidatorSpec(ctx, client, validatorNS, validatorCronJob)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ctx
}

// Transient errors on the CronJob read and on the Job create are retried.
func TestWatchValidatorSpec_RetriesTransientErrors(t *testing.T) {
	fastValidatorPolls(t)
	client, _ := validatorClient(validatorCronJobWithSpec("a"))
	var gets, creates atomic.Int32
	client.PrependReactor("get", "cronjobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if gets.Add(1) == 1 {
			return true, nil, apierrors.NewInternalError(errors.New("etcd leader change"))
		}
		return false, nil, nil
	})
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if creates.Add(1) == 1 {
			return true, nil, apierrors.NewServerTimeout(batchv1.Resource("jobs"), "create", 1)
		}
		return false, nil, nil
	})
	startWatch(t, client)
	require.Eventually(t, func() bool { return len(validatorJobs(t, client)) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.GreaterOrEqual(t, gets.Load(), int32(3))
	assert.Equal(t, int32(2), creates.Load())
}

// The release applies the CronJob beside the operator in no fixed order, so
// a missing CronJob is waited for.
func TestWatchValidatorSpec_WaitsForTheCronJob(t *testing.T) {
	fastValidatorPolls(t)
	client, _ := validatorClient()
	ctx := startWatch(t, client)
	time.Sleep(100 * time.Millisecond)
	_, err := client.BatchV1().CronJobs(validatorNS).Create(ctx, validatorCronJobWithSpec("a"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(validatorJobs(t, client)) == 1 }, 5*time.Second, 10*time.Millisecond)
}

// A denied read or create stops the watch: retrying cannot fix RBAC.
func TestWatchValidatorSpec_ForbiddenStops(t *testing.T) {
	fastValidatorPolls(t)
	for _, verb := range []string{"get", "create"} {
		client, _ := validatorClient(validatorCronJobWithSpec("a"))
		resource := map[string]string{"get": "cronjobs", "create": "jobs"}[verb]
		client.PrependReactor(verb, resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(batchv1.Resource(resource), "", errors.New("rbac"))
		})
		done := make(chan struct{})
		go func() {
			watchValidatorSpec(context.Background(), client, validatorNS, validatorCronJob)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("a forbidden %s did not stop the watch", verb)
		}
		assert.Equal(t, 1, countVerb(client, "get", "cronjobs"), verb)
	}
}

// The watch ends with the operator's context.
func TestWatchValidatorSpec_StopsWithTheContext(t *testing.T) {
	client, _ := validatorClient()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchValidatorSpec(ctx, client, validatorNS, validatorCronJob)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the watch outlived its context")
	}
}

// The run's name fits the 63 characters the job-name label allows, whatever
// the CronJob is called, and is the same for racing replicas but differs for
// another spec or another predecessor.
func TestValidatorRunJobName(t *testing.T) {
	long := strings.Repeat("a", 49) + "-" + strings.Repeat("b", 10)
	name := validatorRunJobName(long, "spec", "uid-1")
	assert.LessOrEqual(t, len(name), 63)
	assert.True(t, strings.HasPrefix(name, strings.Repeat("a", 49)+"-run-"), "a cut name ends without a dash: %s", name)
	assert.Equal(t, name, validatorRunJobName(long, "spec", "uid-1"))
	assert.NotEqual(t, name, validatorRunJobName(long, "other", "uid-1"))
	assert.NotEqual(t, name, validatorRunJobName(long, "spec", "uid-2"))
}
