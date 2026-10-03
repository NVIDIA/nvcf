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
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
)

// validatorSpecAnnotation identifies the validator spec a run used: its Job
// spec and its network-checks ConfigMap. The chart stamps it on the
// CronJob's job template, so scheduled Jobs carry it as well as the ones the
// operator starts.
const validatorSpecAnnotation = "nvca.nvcf.nvidia.io/cluster-validator-spec"

// validatorRunPollInterval is how often the operator re-reads the CronJob. A
// var so tests can shorten it.
var validatorRunPollInterval = 30 * time.Second

// watchValidatorSpec runs the cluster-validator CronJob as a Job whenever the
// newest Job the CronJob owns did not run its current spec: at install, and
// after every upgrade or rollback that changes what the validator runs or
// reads. Under the control-plane role the operator's init container publishes
// no summary, so without this the first summary, and every change to it,
// would wait for the schedule.
//
// The operator starts the run rather than the chart rendering it: a Job in
// the release made the install depend on the validator, so an image the
// cluster could not pull, or a pod that could not be scheduled, failed the
// install, and so would a hook. Polling the CronJob rather than restarting
// the operator keeps a validator-only change from rolling the operator
// through its init container.
//
// It returns when ctx ends or the operator is not allowed to read the CronJob
// or create the Job. Every other error is retried at the next poll; the
// CronJob's own schedule runs regardless.
func watchValidatorSpec(ctx context.Context, client kubernetes.Interface, namespace, cronJobName string) {
	w := &validatorRunWatcher{
		client:    client,
		namespace: namespace,
		name:      cronJobName,
		log:       core.GetLogger(ctx).WithField("cronjob", namespace+"/"+cronJobName),
	}
	for !w.reconcile(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(validatorRunPollInterval):
		}
	}
}

type validatorRunWatcher struct {
	client          kubernetes.Interface
	namespace, name string
	log             *logrus.Entry

	// ran is the spec the newest run is known to have used.
	ran string
	// reported is the last skip reason logged, so a wait is logged once.
	reported string
	// failing is set while API errors repeat, so only the first is a warning.
	failing bool
}

// reconcile starts a run if the newest owned Job did not run the CronJob's
// current spec and nothing is running. It reports whether to stop watching.
func (w *validatorRunWatcher) reconcile(ctx context.Context) (stop bool) {
	cronJob, err := w.client.BatchV1().CronJobs(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
	if err != nil {
		return w.apiError("read the cluster-validator CronJob", err)
	}
	spec := cronJob.Spec.JobTemplate.Annotations[validatorSpecAnnotation]
	switch {
	case spec == "":
		w.skip("", "the CronJob carries no "+validatorSpecAnnotation+" annotation, so the spec a run used is unknown")
		return false
	case spec == w.ran:
		return false
	case cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend:
		w.skip(spec, "the CronJob is suspended")
		return false
	case len(cronJob.Status.Active) > 0:
		w.skip(spec, "a scheduled run is in progress")
		return false
	}

	jobs, err := w.client.BatchV1().Jobs(w.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(cronJob.Spec.JobTemplate.Labels).String(),
	})
	if err != nil {
		return w.apiError("list the cluster-validator Jobs", err)
	}
	owned := ownedJobs(jobs.Items, cronJob)
	for _, job := range owned {
		if !jobFinished(job) {
			w.skip(spec, "run "+job.Name+" is in progress")
			return false
		}
	}
	newest := ""
	if len(owned) > 0 {
		last := owned[len(owned)-1]
		if last.Annotations[validatorSpecAnnotation] == spec {
			w.ran, w.failing = spec, false
			return false
		}
		newest = string(last.UID)
	}
	return w.start(ctx, cronJob, spec, newest)
}

func (w *validatorRunWatcher) start(ctx context.Context, cronJob *batchv1.CronJob, spec, newest string) bool {
	job := validatorRunJob(cronJob, spec, newest)
	_, err := w.client.BatchV1().Jobs(w.namespace).Create(ctx, job, metav1.CreateOptions{})
	switch {
	case err == nil:
		w.log.Infof("started cluster-validator run %s for validator spec %s", job.Name, spec)
	case apierrors.IsAlreadyExists(err):
		// Another operator replica started it, or a Job of that name exists
		// that the CronJob does not own. Either way it is left alone.
		existing, getErr := w.client.BatchV1().Jobs(w.namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if getErr == nil && !ownedBy(existing, cronJob) {
			w.log.Warnf("Job %s exists but the cluster-validator CronJob does not own it; not starting a run "+
				"for validator spec %s", job.Name, spec)
		}
	default:
		return w.apiError("start a cluster-validator run", err)
	}
	w.ran, w.failing = spec, false
	return false
}

// apiError logs err and reports whether to stop: only a denied request stops
// the watch, since retrying cannot fix it.
func (w *validatorRunWatcher) apiError(action string, err error) bool {
	log := w.log.WithError(err)
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		log.Warnf("not allowed to %s; the operator starts no cluster-validator runs, "+
			"and the summary waits for the CronJob's schedule", action)
		return true
	case apierrors.IsNotFound(err):
		log.Debugf("could not %s yet; waiting for it", action)
	case w.failing:
		log.Debugf("could not %s; retrying", action)
	default:
		w.failing = true
		log.Warnf("could not %s; retrying every %s", action, validatorRunPollInterval)
	}
	return false
}

func (w *validatorRunWatcher) skip(spec, reason string) {
	if key := spec + "/" + reason; key != w.reported {
		w.reported = key
		w.log.Infof("not starting a cluster-validator run for validator spec %s: %s", spec, reason)
	}
}

// ownedJobs returns the Jobs cronJob controls, oldest first.
func ownedJobs(jobs []batchv1.Job, cronJob *batchv1.CronJob) []*batchv1.Job {
	var owned []*batchv1.Job
	for i := range jobs {
		if ownedBy(&jobs[i], cronJob) {
			owned = append(owned, &jobs[i])
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		a, b := owned[i].CreationTimestamp, owned[j].CreationTimestamp
		if !a.Equal(&b) {
			return a.Before(&b)
		}
		return owned[i].Name < owned[j].Name
	})
	return owned
}

func ownedBy(job *batchv1.Job, cronJob *batchv1.CronJob) bool {
	ref := metav1.GetControllerOf(job)
	return ref != nil && ref.UID == cronJob.UID
}

func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// validatorRunJob builds the Job the way kubectl create job --from=cronjob
// does: the CronJob's job template, controlled by the CronJob, so its history
// limits and an uninstall clean it up like any scheduled run. The CronJob
// controller did not create it, so while it runs the controller records an
// UnexpectedJob event on the CronJob; that event is expected. For the same
// reason the CronJob's concurrencyPolicy does not see it: a scheduled run that
// falls due meanwhile runs beside it. Each run probes in namespaces of its
// own, and the summary is whichever run writes last.
func validatorRunJob(cronJob *batchv1.CronJob, spec, newest string) *batchv1.Job {
	annotations := map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	for k, v := range cronJob.Spec.JobTemplate.Annotations {
		annotations[k] = v
	}
	jobLabels := map[string]string{}
	for k, v := range cronJob.Spec.JobTemplate.Labels {
		jobLabels[k] = v
	}
	controller := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        validatorRunJobName(cronJob.Name, spec, newest),
			Namespace:   cronJob.Namespace,
			Labels:      jobLabels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       cronJob.Name,
				UID:        cronJob.UID,
				Controller: &controller,
			}},
		},
		Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
	}
}

// validatorRunJobName is the CronJob's name and a hash of the spec and of the
// Job it follows, within the 63 characters the job-name label allows. Replicas
// racing for the same run pick the same name; a later return to the same spec
// picks a new one.
func validatorRunJobName(cronJobName, spec, newest string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(spec + "/" + newest))
	suffix := fmt.Sprintf("-run-%08x", h.Sum32())
	base := cronJobName
	if limit := 63 - len(suffix); len(base) > limit {
		base = strings.TrimRight(base[:limit], "-")
	}
	return base + suffix
}

// deleteLeftoverValidatorSummary removes the summary a cluster-validator wrote
// while it was enabled. Nothing updates it once the validator is disabled, so
// an agent that still reads it, such as one older than this operator, would
// republish its old last_run and fire the staleness alert; and were the
// validator enabled again, it would do the same until the first new run. Only
// a ConfigMap the validator wrote is deleted. Failures are logged and
// otherwise ignored.
func deleteLeftoverValidatorSummary(ctx context.Context, client kubernetes.Interface, namespace string) {
	if namespace == "" {
		return
	}
	log := core.GetLogger(ctx).WithField("configmap", namespace+"/"+clustervalidator.SummaryConfigMapName)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	configMaps := client.CoreV1().ConfigMaps(namespace)
	cm, err := configMaps.Get(ctx, clustervalidator.SummaryConfigMapName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return
	case err != nil:
		log.WithError(err).Warn("could not read the summary of a disabled cluster-validator to delete it")
		return
	case cm.Labels["app.kubernetes.io/managed-by"] != clustervalidator.SummaryManagedBy:
		return
	}
	err = configMaps.Delete(ctx, cm.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cm.UID}})
	if err != nil && !apierrors.IsNotFound(err) {
		log.WithError(err).Warn("could not delete the summary of a disabled cluster-validator")
		return
	}
	log.Info("deleted the summary of a disabled cluster-validator")
}
