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
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// initialValidatorRunWait bounds how long the operator waits for the
// cluster-validator CronJob to appear: the release applies it beside the
// operator's Deployment, in no guaranteed order. Vars so tests can shorten.
var (
	initialValidatorRunWait     = 5 * time.Minute
	initialValidatorRunInterval = 5 * time.Second
)

// startInitialValidatorRun runs the cluster-validator CronJob once, as a Job,
// when the operator starts. Under the control-plane role the operator's init
// container publishes no summary, so without this the first summary waits for
// the CronJob's first tick, up to a full schedule interval.
//
// The operator starts it rather than the chart rendering it: a Job in the
// release made the install depend on the validator, so an image the cluster
// could not pull, or a pod that could not be scheduled, failed the install
// under helm --wait-for-jobs, and so would a hook. The Job is named for the
// CronJob's job template, so it runs once per distinct validator spec (an
// upgrade that changes it runs again), not on every operator restart. Failures
// are logged and otherwise ignored: the CronJob's own schedule still runs.
func startInitialValidatorRun(ctx context.Context, client kubernetes.Interface, namespace, cronJobName string) {
	log := core.GetLogger(ctx).WithField("cronjob", namespace+"/"+cronJobName)
	deadline := time.Now().Add(initialValidatorRunWait)
	for {
		cronJob, err := client.BatchV1().CronJobs(namespace).Get(ctx, cronJobName, metav1.GetOptions{})
		if err == nil {
			job := initialValidatorJob(cronJob)
			_, err = client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
			switch {
			case err == nil:
				log.Infof("started the initial cluster-validator run %s", job.Name)
			case apierrors.IsAlreadyExists(err):
				log.Debugf("initial cluster-validator run %s already exists for this validator spec", job.Name)
			default:
				log.WithError(err).Warn("could not start the initial cluster-validator run; " +
					"the first summary waits for the CronJob's schedule")
			}
			return
		}
		if !apierrors.IsNotFound(err) || time.Now().After(deadline) {
			log.WithError(err).Warn("could not read the cluster-validator CronJob to start its initial run; " +
				"the first summary waits for the CronJob's schedule")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialValidatorRunInterval):
		}
	}
}

// initialValidatorJob builds the Job the way kubectl create job --from=cronjob
// does: the CronJob's job template, owned by the CronJob, so its history
// limits and an uninstall clean it up like any scheduled run.
func initialValidatorJob(cronJob *batchv1.CronJob) *batchv1.Job {
	spec := cronJob.Spec.JobTemplate.Spec.DeepCopy()
	annotations := map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	for k, v := range cronJob.Spec.JobTemplate.Annotations {
		annotations[k] = v
	}
	labels := map[string]string{}
	for k, v := range cronJob.Spec.JobTemplate.Labels {
		labels[k] = v
	}
	controller := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        initialValidatorJobName(cronJob.Name, spec),
			Namespace:   cronJob.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cronJob.Name,
				UID:        cronJob.UID,
				Controller: &controller,
			}},
		},
		Spec: *spec,
	}
}

// initialValidatorJobName is the CronJob's name and a hash of its job template,
// within the 63 characters the job-name label allows.
func initialValidatorJobName(cronJobName string, spec *batchv1.JobSpec) string {
	h := fnv.New32a()
	raw, _ := json.Marshal(spec)
	_, _ = h.Write(raw)
	suffix := fmt.Sprintf("-initial-%08x", h.Sum32())
	base := cronJobName
	if limit := 63 - len(suffix); len(base) > limit {
		base = strings.TrimRight(base[:limit], "-")
	}
	return base + suffix
}
