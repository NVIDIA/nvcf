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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func validatorCronJob(image string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nvca-operator-cluster-validator", Namespace: "nvca-operator", UID: types.UID("cj-uid"),
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 */3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/component": "validation"}},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "cluster-validator", Image: image}},
				}}},
			},
		},
	}
}

func validatorJobs(t *testing.T, client *fake.Clientset) []batchv1.Job {
	t.Helper()
	jobs, err := client.BatchV1().Jobs("nvca-operator").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	return jobs.Items
}

// The first summary under the control-plane role comes from a Job the operator
// starts from the CronJob's template, owned by the CronJob. It runs once per
// validator spec: a restart does not run it again, a changed spec does.
func TestStartInitialValidatorRun_OncePerValidatorSpec(t *testing.T) {
	client := fake.NewSimpleClientset(validatorCronJob("validator:1"))
	ctx := context.Background()

	startInitialValidatorRun(ctx, client, "nvca-operator", "nvca-operator-cluster-validator")
	jobs := validatorJobs(t, client)
	require.Len(t, jobs, 1)
	job := jobs[0]
	assert.Equal(t, "validator:1", job.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "validation", job.Labels["app.kubernetes.io/component"])
	assert.Equal(t, "manual", job.Annotations["cronjob.kubernetes.io/instantiate"])
	require.Len(t, job.OwnerReferences, 1)
	assert.Equal(t, "CronJob", job.OwnerReferences[0].Kind)
	assert.Equal(t, types.UID("cj-uid"), job.OwnerReferences[0].UID)

	startInitialValidatorRun(ctx, client, "nvca-operator", "nvca-operator-cluster-validator")
	assert.Len(t, validatorJobs(t, client), 1, "an operator restart does not run it again")

	_, err := client.BatchV1().CronJobs("nvca-operator").Update(ctx, validatorCronJob("validator:2"),
		metav1.UpdateOptions{})
	require.NoError(t, err)
	startInitialValidatorRun(ctx, client, "nvca-operator", "nvca-operator-cluster-validator")
	assert.Len(t, validatorJobs(t, client), 2, "an upgrade that changes the validator runs it again")
}

// The release applies the CronJob beside the operator in no guaranteed order,
// so the operator waits for it, but not forever.
func TestStartInitialValidatorRun_WaitsForTheCronJob(t *testing.T) {
	prevWait, prevInterval := initialValidatorRunWait, initialValidatorRunInterval
	initialValidatorRunWait, initialValidatorRunInterval = 2*time.Second, 50*time.Millisecond
	t.Cleanup(func() { initialValidatorRunWait, initialValidatorRunInterval = prevWait, prevInterval })

	client := fake.NewSimpleClientset()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = client.BatchV1().CronJobs("nvca-operator").Create(context.Background(),
			validatorCronJob("validator:1"), metav1.CreateOptions{})
	}()
	startInitialValidatorRun(context.Background(), client, "nvca-operator", "nvca-operator-cluster-validator")
	assert.Len(t, validatorJobs(t, client), 1)

	initialValidatorRunWait = 200 * time.Millisecond
	start := time.Now()
	startInitialValidatorRun(context.Background(), fake.NewSimpleClientset(), "nvca-operator", "missing")
	assert.Less(t, time.Since(start), 2*time.Second, "a CronJob that never appears is given up on")
}

// The Job name fits the 63 characters the job-name label allows, whatever the
// CronJob is called, and depends on the job template.
func TestInitialValidatorJobName(t *testing.T) {
	spec := &validatorCronJob("validator:1").Spec.JobTemplate.Spec
	long := strings.Repeat("a", 52)
	name := initialValidatorJobName(long, spec)
	assert.LessOrEqual(t, len(name), 63)
	assert.True(t, strings.HasPrefix(name, "aaaa"))
	assert.NotEqual(t, name, initialValidatorJobName(long, &validatorCronJob("validator:2").Spec.JobTemplate.Spec))
	assert.Equal(t, name, initialValidatorJobName(long, spec.DeepCopy()))
}
