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

package selfhosted

import (
	"cmp"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	clusterValidatorNamespace  = "default"
	clusterValidatorName       = "nvcf-preflight-validator"
	clusterValidatorContainer  = "validator"
	clusterValidatorAppLabel   = "nvcf-cluster-validator"
	clusterValidatorTTLSeconds = int32(600)
	clusterValidatorHintURL    = "https://docs.nvidia.com/nvcf/self-managed-clusters#cluster-validator"
	// orphanValidatorRBACTTL is the minimum age before leftover validator RBAC
	// is reclaimed. Must exceed the validator timeout so a live run is never hit.
	orphanValidatorRBACTTL = 30 * time.Minute
	// preservedValidatorTTL bounds how long --no-cleanup artifacts survive.
	// Exempting them outright left a cluster-wide ClusterRole and an NGC-key
	// Secret in place forever once nobody ran the check again.
	preservedValidatorTTL = 24 * time.Hour
	// clusterValidatorManagedBy tells this CLI's per-run objects apart from the
	// fixed-name set released CLIs create. Those releases DeleteCollection on
	// managed-by=nvcf-cli, so sharing the value let each version delete the
	// other's in-flight Jobs, Secrets and RBAC.
	clusterValidatorManagedBy = "nvcf-cli-validator"
	// clusterValidatorRunLabel carries the run ID on everything one run
	// creates, so the cleanup hint can address exactly that run's objects.
	clusterValidatorRunLabel = "nvcf.nvidia.com/validator-run"
)

// Vars (not consts) so tests can shorten without the full production budget.
var (
	clusterValidatorTimeout         = 5 * time.Minute
	clusterValidatorPollInterval    = 2 * time.Second
	clusterValidatorLogFetchTimeout = 10 * time.Second
	// validatorCleanupTimeout bounds each deferred sweep. They run after an
	// interrupt too, and a second signal or CI's follow-up SIGTERM can end the
	// process at any time, so an unreachable apiserver must not hold it open.
	validatorCleanupTimeout = 30 * time.Second
)

// validatorDeferredSweeps is how many deferred sweeps one run can make, each
// bounded by validatorCleanupTimeout: the pull secret, the RBAC, and for the
// control-plane role the network-checks ConfigMap.
const validatorDeferredSweeps = 3

// validatorGradeReads is how many calls, each bounded by
// clusterValidatorLogFetchTimeout, one run can make outside its own timeout:
// the ownership patch, the transcript, the transcript again for a validator
// that finished late, and the container's exit code.
const validatorGradeReads = 4

// validatorDeadlineOffset is how long after the end of the run's own timeout
// the Job's active deadline ends its pod. The deadline plus the pod's
// termination grace must fall inside validatorDeadlineGrace, the wait for it.
const validatorDeadlineOffset = 60 * time.Second

// podTerminationGrace is the pod's default terminationGracePeriodSeconds,
// which the validator Job does not change.
const podTerminationGrace = 30 * time.Second

// ClusterValidatorRunCeiling is the longest one validator run can take: its
// own timeout, then on a timeout the wait for the Job's deadline to end the
// pod and the reads that grade it, then the deferred sweeps, which run in turn
// on fresh contexts, plus a margin for the pod's termination grace period.
func ClusterValidatorRunCeiling() time.Duration {
	return clusterValidatorTimeout + validatorDeadlineGrace + validatorGradeReads*clusterValidatorLogFetchTimeout +
		validatorDeferredSweeps*validatorCleanupTimeout + validatorRunMargin
}

// cleanupContext is a fresh, bounded context for one deferred sweep. It does
// not derive from the run's ctx, which may already be cancelled.
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), validatorCleanupTimeout)
}

// Kubelet Waiting.Reason values that mean the pod will never start without
// operator intervention. Detecting any short-circuits the 5-minute wait.
var pullFailureReasons = map[string]struct{}{
	"ImagePullBackOff":                {},
	"ErrImagePull":                    {},
	"FailedToRetrieveImagePullSecret": {},
	"InvalidImageName":                {},
	"ImageInspectError":               {},
	"RegistryUnavailable":             {},
}

var (
	ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	boxDrawingRE = regexp.MustCompile(`[\x{2500}-\x{257F}]+`)
	blankLineRE  = regexp.MustCompile(`\n{3,}`)
)

type ClusterValidatorParams struct {
	KubeContext string
	Image       string
	PullSecret  string
	NoCleanup   bool
	// Role selects which check set the validator runs: "control-plane" or
	// "compute-plane" (empty = compute-plane default). Passed to the Job as
	// VALIDATOR_ROLE. See clustervalidator.RoleControlPlane / RoleComputePlane.
	Role string
	// Registries are probed for reachability by the control-plane validator:
	// the same list, with the same criticality, the local credential check
	// uses. Ignored for the compute-plane role.
	Registries []RegistryEntry
	// Env is passed to the validator container: namespace overrides, the
	// probe image, and whether the control plane is expected to be installed.
	Env map[string]string
	// Tolerations are added to the control-plane ones the Job always
	// carries, for clusters whose nodes use other taints, as the chart's
	// clusterValidator.tolerations does.
	Tolerations []corev1.Toleration
	// OnStart, when set, receives the run's ID before the run creates
	// anything, so the caller can say how to remove the run's objects if the
	// process is stopped before the run's own teardown ends.
	OnStart func(runID string)
}

// Err is non-nil only when the run failed to execute (RBAC bootstrap,
// image pull, context timeout). A Passed=false verdict from the validator
// itself leaves Err nil.
type ClusterValidatorResult struct {
	Passed   bool
	ExitCode int32
	// Reason is why the validator container or its pod ended, when the
	// cluster says more than its exit code: OOMKilled, Evicted.
	Reason string
	Logs   string
	// LogsErr is why the transcript could not be read, if it could not.
	LogsErr error
	JobName string
	// RunID labels every object the run created (clusterValidatorRunLabel).
	RunID string
	// Created reports that the run created at least one object in the
	// cluster, so a kept run has something to remove.
	Created bool
	Err     error
	// LeftBehind reports that this run's RBAC and other objects were kept
	// because its pod could still be running, so the caller can print the
	// command that removes them rather than leave them for the orphan sweep.
	LeftBehind bool
	// SweepErr is why removing the run's objects failed. They are still in
	// the cluster, so the caller prints the command that removes them.
	SweepErr error
	// Suspended reports that a kept Job whose pod could not pull was
	// suspended, so the pod was deleted and has no logs to read.
	Suspended bool
}

type ClusterValidator func(ctx context.Context, params ClusterValidatorParams) ClusterValidatorResult

// NewClusterValidator returns a ClusterValidator backed by client-go.
// Chart-independent so it works before any Helm release exists.
func NewClusterValidator() ClusterValidator {
	return func(ctx context.Context, p ClusterValidatorParams) ClusterValidatorResult {
		restCfg, err := loadKubeConfig(p.KubeContext)
		if err != nil {
			return ClusterValidatorResult{Err: fmt.Errorf("building kubeconfig: %w", err)}
		}
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return ClusterValidatorResult{Err: fmt.Errorf("building kubernetes client: %w", err)}
		}
		return runValidatorJob(ctx, client, p)
	}
}

// runClusterValidator is runValidatorJob with the parameters spelled out.
func runClusterValidator(
	ctx context.Context, client kubernetes.Interface, image, pullSecret string, noCleanup bool, role string,
	registries []RegistryEntry, env map[string]string, tolerations ...corev1.Toleration,
) ClusterValidatorResult {
	return runValidatorJob(ctx, client, ClusterValidatorParams{
		Image: image, PullSecret: pullSecret, NoCleanup: noCleanup, Role: role,
		Registries: registries, Env: env, Tolerations: tolerations,
	})
}

// Testable core. Pass a fake clientset to unit-test without a real cluster.
// The result is named so the deferred sweeps can report what they could not
// remove.
func runValidatorJob(ctx context.Context, client kubernetes.Interface, p ClusterValidatorParams) (res ClusterValidatorResult) {
	image, pullSecret, noCleanup, role := p.Image, p.PullSecret, p.NoCleanup, p.Role
	if image == "" {
		// Defensive: callers gate on configured image before invoking the
		// validator, so this branch shouldn't fire in normal use.
		return ClusterValidatorResult{Err: fmt.Errorf("cluster-validator image is empty")}
	}

	vctx, cancel := context.WithTimeout(ctx, clusterValidatorTimeout)
	defer cancel()

	// podMayBeRunning is true only between a successful Job create and the
	// point its pods are seen to end. Every cleanup defer below keys off it:
	// outside that window nothing is using the pull secret or the RBAC, so
	// reclaiming them is safe, and inside it reclaiming them breaks the pod
	// that is still running.
	podMayBeRunning := false

	// Random per-run identity. Every object this run creates carries it, so a
	// predictable name cannot be pre-created by someone else and then either
	// bound to the validator's cluster-wide permissions or overwritten with the
	// NGC credential. Generated before anything is created, because the pull
	// secret name needs it too.
	runID, err := newValidatorRunID()
	if err != nil {
		return ClusterValidatorResult{Err: err}
	}
	if p.OnStart != nil {
		p.OnStart(runID)
	}

	// Reclaim leftovers from dead runs before resolving this run's pull
	// secret. The sweep only takes objects older than its TTL, so it cannot
	// reach anything this run is about to create.
	sweepOrphanClusterValidatorRBAC(vctx, client, orphanValidatorRBACTTL)

	// Resolver errors are non-fatal: fall through to the caller's value and
	// let waitForClusterValidatorJob surface ImagePullBackOff if needed.
	if resolved, err := resolveValidatorPullSecret(ctx, client, pullSecret, image, role, runID, noCleanup); err == nil {
		pullSecret = resolved
	}
	mintedSecret := pullSecret != "" && pullSecret == validatorPullSecretRunName(role, runID)

	// sweep runs one deferred sweep on a fresh, bounded context, and only once
	// no pod of this run can still need what it removes. --no-cleanup keeps
	// everything. A sweep that fails leaves objects behind, so the result
	// carries its error and the caller prints the removal command.
	sweep := func(remove func(context.Context) error) {
		if noCleanup || podMayBeRunning {
			return
		}
		cctx, cancel := cleanupContext()
		defer cancel()
		if err := remove(cctx); err != nil {
			res.SweepErr = errors.Join(res.SweepErr, err)
			res.Created = true
		}
	}
	// Sweep only this run's managed pull secret, and only once the pod can no
	// longer need it. Sweeping unconditionally deletes the Secret out from
	// under a pod that is still retrying its pull, which the kubelet then
	// reports as FailedToRetrieveImagePullSecret. Operator-supplied secrets via
	// the flag aren't labeled by us and are skipped.
	defer sweep(func(c context.Context) error { return sweepManagedPullSecrets(c, client, role, runID) })

	// Register the cleanup before the bootstrap, not after. The bootstrap
	// creates three objects in sequence and returns on the first failure, so a
	// kubeconfig that can create a ServiceAccount but not a cluster-scoped
	// ClusterRole would otherwise abandon the ServiceAccount on every attempt.
	// The sweep is name-scoped and label-guarded, so running it when nothing
	// was created is a no-op.
	defer sweep(func(c context.Context) error { return sweepClusterValidatorRBAC(c, client, role, runID) })
	if created, err := bootstrapClusterValidatorRBAC(vctx, client, role, runID, noCleanup); err != nil {
		// RunID lets --no-cleanup print the command that removes whatever the
		// failed bootstrap and the pull-secret step already created.
		return ClusterValidatorResult{
			RunID: runID, Created: created || mintedSecret, Err: fmt.Errorf("bootstrapping validator RBAC: %w", err),
		}
	}

	// For the control-plane role, create a ConfigMap with reachability
	// endpoints and enforcement config so the validator runs its configurable
	// checks. Best-effort: a failure here is logged but does not abort the
	// run - the validator gracefully skips configurable checks when the
	// ConfigMap is absent.
	var configNote string
	if role == clusterValidatorControlPlaneRole {
		// The ConfigMap is the one object this run creates that had no cleanup
		// path, so it was left in the cluster forever. Same gating as the
		// other sweeps: keep it when the pod may still read it, or when the
		// operator asked to keep the run's artifacts.
		defer sweep(func(c context.Context) error { return sweepClusterValidatorConfig(c, client, runID) })
		if err := ensureClusterValidatorConfig(vctx, client, p.Registries, runID, noCleanup); err != nil {
			// Non-fatal: continue without the ConfigMap; the validator skips
			// configurable reachability and enforcement checks silently unless
			// we surface this note in the transcript.
			configNote = fmt.Sprintf("note: validator config not applied (%v); reachability checks may be skipped", err)
		}
	}

	// No sweep of earlier Jobs: every Job now carries a deadline and a TTL,
	// so a role-wide sweep could only ever hit an overlapping run's live Job.
	jobName := fmt.Sprintf("%s-%d", clusterValidatorName, time.Now().UnixNano())
	spec := buildClusterValidatorJob(jobName, image, pullSecret, role, runID, noCleanup, p.Env, p.Tolerations...)
	if spec.Spec.ActiveDeadlineSeconds != nil {
		// Counted from now, not from the start of the run: the sweep, the
		// pull-secret scan and the bootstrap have used some of its timeout.
		deadline := validatorActiveDeadlineSeconds(timeLeft(vctx))
		spec.Spec.ActiveDeadlineSeconds = &deadline
	}
	job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Create(vctx, spec, metav1.CreateOptions{})
	if err != nil {
		// A create cut off by an interrupt or a dropped connection may still
		// have been applied. Without --no-cleanup its pod would run with its
		// RBAC swept, so it is stopped, and when its pod may still be running
		// its objects are kept and the result says how to remove them. Under
		// --no-cleanup it is kept, but suspended: nothing follows it, and with
		// no deadline a pod that cannot pull would retry forever.
		res := ClusterValidatorResult{RunID: runID, Created: true, Err: fmt.Errorf("creating validator Job: %w", err)}
		if noCleanup {
			if serr := suspendValidatorJob(client, jobName); serr != nil && !apierrors.IsNotFound(serr) {
				res.Err = fmt.Errorf("%w; suspending the kept Job failed: %v", res.Err, serr)
			}
			return res
		}
		podMayBeRunning = !stopValidatorJob(client, jobName)
		res.LeftBehind = podMayBeRunning
		return res
	}
	podMayBeRunning = true

	// The Job owns this run's pull secret and ConfigMap, so they go when the
	// Job goes, even on paths where the deferred sweeps are suppressed: its
	// TTL removes it, or for --no-cleanup a later check's orphan sweep once
	// preservedValidatorTTL has passed. The ServiceAccount is
	// deliberately not owned: the ClusterRole and binding are cluster-scoped
	// and cannot be, and deleting the account alone would leave a binding
	// that anyone able to recreate that ServiceAccount name could inherit.
	// Not under --no-cleanup: a kept Secret or ConfigMap owned by the kept Job
	// would be garbage-collected the moment an operator re-runs that Job by
	// deleting and recreating it. On a fresh context: an interrupt right after
	// the create must not leave them unowned.
	var owned []ownedArtifact
	if mintedSecret {
		owned = append(owned, ownedArtifact{kind: "Secret", name: pullSecret})
	}
	if role == clusterValidatorControlPlaneRole && configNote == "" {
		owned = append(owned, ownedArtifact{kind: "ConfigMap", name: clusterValidatorConfigRunName(runID)})
	}
	if !noCleanup {
		octx, ocancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
		ownByJob(octx, client, job, owned)
		ocancel()
	}

	final, waitErr := waitForClusterValidatorJob(vctx, client, jobName)
	// The run's own context has ended: the operator interrupted it, or the
	// check's time budget ran out. Nothing will wait for a later result, so
	// the Job is stopped now rather than left to its deadline.
	abandoned := waitErr != nil && ctx.Err() != nil
	var pullErr *validatorImagePullError
	isPullErr := errors.As(waitErr, &pullErr)
	ownTimeout := waitErr != nil && !abandoned && !isPullErr && errors.Is(waitErr, context.DeadlineExceeded)
	if ownTimeout {
		// The validator's own timeout, not the check's budget: its result is
		// that it did not finish, which is not the budget cutting it short.
		waitErr = fmt.Errorf("the validator did not finish within %s", clusterValidatorTimeout)
	}

	// Read the transcript before anything stops the pod, on a fresh context:
	// the run's own may be over, and a run that timed out is where the
	// partial transcript matters most. Not after an interrupt, which only
	// wants the cleanup.
	var rawLogs string
	var logsErr error
	if !errors.Is(ctx.Err(), context.Canceled) {
		rawLogs, logsErr = readValidatorLogs(client, jobName)
	}

	// A pull failure is different: the container never started, so nothing
	// uses the RBAC, the pull secret or the ConfigMap. Delete the Job so its
	// pod stops retrying, and let the deferred sweeps run now rather than
	// leaving a cluster-wide ClusterRole and an NGC-key Secret for a later
	// run's orphan sweep, or for nobody. A kept Job is suspended instead.
	// Each stop deletes the Job in the foreground and waits, bounded, for its
	// pod to end before the deferred sweeps revoke its RBAC. The validator
	// removes its own probe namespace and DaemonSet on SIGTERM, and needs
	// that RBAC to do it.
	stopped, suspended := false, false
	var suspendErr error
	// endRun stops the Job, or under --no-cleanup suspends it when its pod is
	// failing to pull: a kept Job has no deadline, so that pod would retry
	// forever.
	endRun := func(pullFailing bool) {
		switch {
		case !noCleanup:
			podMayBeRunning = !stopValidatorJob(client, jobName)
			stopped = true
		case pullFailing || validatorPodPullFailing(client, jobName):
			suspended, suspendErr = true, suspendValidatorJob(client, jobName)
		}
	}
	switch {
	case waitErr == nil:
		// The Job counts an evicted or preempted pod as failed while the pod
		// still runs its SIGTERM cleanup, which needs the RBAC.
		if !noCleanup {
			podMayBeRunning = !waitValidatorPodsDone(client, jobName, validatorStopTimeout)
		}
	case isPullErr || abandoned:
		endRun(isPullErr)
	default:
		// The validator's own timeout passed, or its Job could not be read
		// for a while. Its active deadline ends the pod soon after the
		// timeout, so wait for the Job to finish, then grade on what it
		// actually did: a validator that finished late, or while its Job
		// could not be read, still has a result. The Job is the authority:
		// its status can trail its pod's, and before its pod exists an empty
		// pod list proves nothing. An interrupt or the end of the check's
		// budget during the wait ends the run instead.
		job, podsDone, ended := awaitValidatorJob(ctx, client, jobName, validatorDeadlineGrace+timeLeft(vctx))
		podMayBeRunning = !podsDone
		switch {
		case ended:
			endRun(false)
			if !ownTimeout {
				// The result was still to come when the run ended, so this
				// is the interrupt or the budget, not the validator.
				waitErr = fmt.Errorf("waiting for job %s: %w (after %w)", jobName, ctx.Err(), waitErr)
			}
		case job != nil:
			final, waitErr = job, nil
			// The partial transcript stays unless the full one can be read.
			if logs, err := readValidatorLogs(client, jobName); logs != "" {
				rawLogs, logsErr = logs, nil
			} else if err != nil {
				logsErr = err
			}
		}
	}

	if waitErr == nil && jobDeadlineExceeded(final) {
		// The deadline stopped a validator that never finished: that is its
		// own timeout, not failed checks.
		waitErr = fmt.Errorf("the validator did not finish within %s (stopped at its active deadline)",
			clusterValidatorTimeout)
	}

	cleaned := cleanValidatorOutput(rawLogs)
	if configNote != "" {
		cleaned = configNote + "\n" + cleaned
	}
	res = ClusterValidatorResult{
		JobName:    jobName,
		RunID:      runID,
		Created:    true,
		Logs:       cleaned,
		LogsErr:    logsErr,
		LeftBehind: podMayBeRunning && !noCleanup,
		Suspended:  suspended && suspendErr == nil,
		Err:        waitErr,
	}
	if suspendErr != nil {
		res.Err = fmt.Errorf("%w; suspending the kept Job failed, so its pod keeps retrying the pull: %v",
			cmp.Or(waitErr, errors.New("the run ended")), suspendErr)
	}
	if stopped {
		// The Job is gone, so a hint to read its logs would 404.
		res.JobName = ""
	}
	if res.Err != nil {
		return res
	}

	// A fresh context: the run's own may have ended while the Job was read.
	exitCtx, exitCancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
	defer exitCancel()
	res.Passed = jobSucceeded(final)
	res.ExitCode, res.Reason = containerTermination(exitCtx, client, jobName)
	return res
}

// timeLeft is how long ctx has before its deadline, or zero.
func timeLeft(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(time.Until(deadline), 0)
	}
	return 0
}

// validatorActiveDeadlineSeconds is the Job's active deadline when left of
// the run's own timeout remains: validatorDeadlineOffset after it ends.
func validatorActiveDeadlineSeconds(left time.Duration) int64 {
	return int64(math.Ceil(left.Seconds())) + int64(validatorDeadlineOffset/time.Second)
}

// readValidatorLogs fetches the validator's transcript on a fresh context
// bounded by clusterValidatorLogFetchTimeout, so it works after the run's own
// context has ended. A failed read is retried inside that bound: a kubelet
// restart or a reset stream is not the transcript.
func readValidatorLogs(client kubernetes.Interface, jobName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
	defer cancel()
	for {
		logs, err := fetchClusterValidatorLogs(ctx, client, jobName)
		if err == nil || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return logs, err
		}
		select {
		case <-ctx.Done():
			return logs, err
		case <-time.After(clusterValidatorPollInterval):
		}
	}
}

// jobFinished reports whether the Job has reached its result: a pod
// succeeded or failed, or it carries a terminal condition.
func jobFinished(job *batchv1.Job) bool {
	if job.Status.Succeeded > 0 || job.Status.Failed > 0 {
		return true
	}
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return true
		}
	}
	return false
}

// jobSucceeded reports whether the validator exited 0.
func jobSucceeded(job *batchv1.Job) bool {
	if job.Status.Succeeded > 0 {
		return true
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// jobDeadlineExceeded reports whether the Job's active deadline ended it.
func jobDeadlineExceeded(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobFailed || c.Type == batchv1.JobFailureTarget) &&
			c.Reason == batchv1.JobReasonDeadlineExceeded {
			return true
		}
	}
	return false
}

// awaitValidatorJob waits up to limit for the Job to finish and its pods to
// end, or until run ends: an interrupt, or the check's budget. It returns the
// Job once it has finished, or nil, whether its pods had all ended, and
// whether run ended first. Reads happen on their own context, so they work
// after run has ended.
func awaitValidatorJob(
	run context.Context, client kubernetes.Interface, jobName string, limit time.Duration,
) (finished *batchv1.Job, podsDone, ended bool) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	for {
		job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err == nil && jobFinished(job) {
			finished = job
		}
		podsDone = validatorPodsEnded(ctx, client, jobName)
		if podsDone && (finished != nil || apierrors.IsNotFound(err)) {
			return finished, true, false
		}
		select {
		case <-run.Done():
			return finished, podsDone, true
		case <-ctx.Done():
			return finished, podsDone, false
		case <-time.After(time.Second):
		}
	}
}

// extraValidatorEnv returns env as container variables in a stable order,
// skipping empty values and any name the Job already sets, so an override can
// never change the role, the config name or preflight mode.
func extraValidatorEnv(base []corev1.EnvVar, env map[string]string) []corev1.EnvVar {
	set := map[string]bool{}
	for _, e := range base {
		set[e.Name] = true
	}
	names := make([]string, 0, len(env))
	for k, v := range env {
		if v != "" && !set[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	out := make([]corev1.EnvVar, 0, len(names))
	for _, k := range names {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return out
}

// ownedArtifact names a namespaced object the validator Job should own.
type ownedArtifact struct{ kind, name string }

// ownByJob sets the Job as the controller-less owner of each artifact, so
// garbage collection removes them with it. Best-effort: a failure only means
// the artifact waits for the deferred or orphan sweep, as before.
func ownByJob(ctx context.Context, client kubernetes.Interface, job *batchv1.Job, artifacts []ownedArtifact) {
	if job == nil || job.UID == "" {
		return
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"ownerReferences": []metav1.OwnerReference{{
			APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
		}},
	}})
	if err != nil {
		return
	}
	for _, a := range artifacts {
		switch a.kind {
		case "Secret":
			_, _ = client.CoreV1().Secrets(clusterValidatorNamespace).Patch(
				ctx, a.name, types.MergePatchType, patch, metav1.PatchOptions{})
		case "ConfigMap":
			_, _ = client.CoreV1().ConfigMaps(clusterValidatorNamespace).Patch(
				ctx, a.name, types.MergePatchType, patch, metav1.PatchOptions{})
		}
	}
}

// deleteValidatorJob removes this run's Job and its pod, but only a Job
// carrying our labels.
func deleteValidatorJob(ctx context.Context, client kubernetes.Interface, name string) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	job, err := jobs.Get(dctx, name, metav1.GetOptions{})
	if err != nil || !hasValidatorManagedLabels(job.Labels) {
		return
	}
	// Foreground: the Job is removed only after its pod, so the pod gets its
	// SIGTERM and grace period while its RBAC still exists.
	propagation := metav1.DeletePropagationForeground
	opts := deleteExactly(job)
	opts.PropagationPolicy = &propagation
	_ = jobs.Delete(dctx, name, opts)
}

// Production values of the waits TestMain shortens, so tests can pin them.
const (
	defaultValidatorDeadlineGrace    = 120 * time.Second
	defaultValidatorPullFailureGrace = 90 * time.Second
)

// Vars (not consts) so tests can shorten them.
var (
	// validatorStopTimeout bounds the wait for a stopped Job's pod to end.
	// It covers the pod's default 30s termination grace period.
	validatorStopTimeout = 45 * time.Second
	// validatorDeadlineGrace bounds the wait, after this run's own timeout,
	// for the Job's active deadline (validatorDeadlineOffset later) to end
	// the pod and for the Job to record it.
	validatorDeadlineGrace = defaultValidatorDeadlineGrace
	// validatorRunMargin is ClusterValidatorRunCeiling's allowance for the
	// pod's termination grace period and the API round trips between waits.
	validatorRunMargin = 30 * time.Second
	// validatorPullFailureGrace is how long a pull failure must persist before
	// it is final. The kubelet's image backoff starts at 10s and doubles, and
	// a failed sync is retried after about 10s more, so its second retry can
	// start 30 to 50s after the first failure. A minute and a half covers
	// it, so a registry outage of under a minute does not fail the run. A
	// pull error that cannot clear (see permanentPullFailure) is final at once.
	validatorPullFailureGrace = defaultValidatorPullFailureGrace
	// validatorGetErrorLimit is how many consecutive failed Job reads end the
	// wait. One 5xx or a refused connection during an apiserver restart is
	// not the Job's result.
	validatorGetErrorLimit = 5
)

// stopValidatorJob deletes the Job and reports whether its pods ended within
// validatorStopTimeout.
func stopValidatorJob(client kubernetes.Interface, name string) bool {
	deleteValidatorJob(context.Background(), client, name)
	return waitValidatorPodsDone(client, name, validatorStopTimeout)
}

// suspendValidatorJob stops a kept Job from creating or retrying its pod.
func suspendValidatorJob(client kubernetes.Interface, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
	defer cancel()
	_, err := client.BatchV1().Jobs(clusterValidatorNamespace).Patch(ctx, name, types.MergePatchType,
		[]byte(`{"spec":{"suspend":true}}`), metav1.PatchOptions{})
	return err
}

// validatorPodPullFailing reports, on a fresh context, whether the Job's pod
// is failing to pull its image.
func validatorPodPullFailing(client kubernetes.Interface, jobName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
	defer cancel()
	reason, _ := podPullFailureReason(ctx, client, jobName)
	return reason != ""
}

// waitValidatorPodsDone waits up to limit, on a fresh context, until no pod of
// the Job can still run, and reports whether that happened.
func waitValidatorPodsDone(client kubernetes.Interface, jobName string, limit time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	for {
		if validatorPodsEnded(ctx, client, jobName) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// validatorPodsEnded reports whether no pod of the Job can still run. A pod
// being deleted may still be running its SIGTERM cleanup, so it has not
// ended. With no pods, that holds only once the Job will create none: it has
// finished, is being deleted, or is gone.
func validatorPodsEnded(ctx context.Context, client kubernetes.Interface, jobName string) bool {
	pods, err := client.CoreV1().Pods(clusterValidatorNamespace).List(ctx,
		metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil {
		return false
	}
	if len(pods.Items) == 0 {
		job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return apierrors.IsNotFound(err)
		}
		return jobFinished(job) || job.DeletionTimestamp != nil
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || (pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
			return false
		}
	}
	return true
}

// validatorImagePullError reports that the validator pod could not pull its
// image, so its container never started.
type validatorImagePullError struct{ reason, message string }

func (e *validatorImagePullError) Error() string {
	return fmt.Sprintf("validator pod cannot pull image (%s): %s", e.reason, e.message)
}

// ensureClusterValidatorRBAC creates this run's ServiceAccount, ClusterRole and
// ClusterRoleBinding for the validator pod. Create only: the names are unique
// per run, so an object that already exists was not made by this run.
func ensureClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface, role, runID string, preserve bool) error {
	_, err := bootstrapClusterValidatorRBAC(ctx, client, role, runID, preserve)
	return err
}

// bootstrapClusterValidatorRBAC is ensureClusterValidatorRBAC that also
// reports whether it created anything before it failed.
func bootstrapClusterValidatorRBAC(
	ctx context.Context, client kubernetes.Interface, role, runID string, preserve bool,
) (created bool, err error) {
	roleLabels := clusterValidatorRunLabels(role, runID, preserve)
	name := clusterValidatorRBACName(role, runID)

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterValidatorNamespace,
			Labels:    roleLabels,
		},
	}
	// No AlreadyExists tolerance: the name carries a random per-run suffix, so
	// anything already sitting there was not put there by this run and must not
	// be adopted and bound to the validator ClusterRole.
	if _, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		return false, fmt.Errorf("create service account: %w", err)
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: roleLabels},
		// Least privilege: only what the validator actually calls. It never
		// creates Services, reads pod logs or watches anything, and it reads
		// GatewayClasses through discovery alone.
		Rules: []rbacv1.PolicyRule{
			// Read-only: cluster inventory, its config ConfigMap, and the
			// LoadBalancer Services the external-LB check inspects.
			{APIGroups: []string{""}, Resources: []string{"nodes", "configmaps", "services"}, Verbs: []string{"get", "list"}},
			// Read + write: the overlay and enforcement probes create and delete
			// their own namespaces and pods.
			{APIGroups: []string{""}, Resources: []string{"namespaces", "pods"}, Verbs: []string{"get", "list", "create", "delete"}},
			// Events, list only: the overlay probe reads its pods' events to
			// tell a slow first image pull, which publishes no pod IP until
			// it returns, from a node that could not network the pod.
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"list"}},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"csidrivers", "storageclasses"}, Verbs: []string{"get", "list"}},
			// NetworkPolicies: read for CNI detection; write for enforcement
			// check which creates/updates/deletes policies in the temp namespace.
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"get", "list", "create", "update", "delete"}},
			{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}, Verbs: []string{"get", "list"}},
			// Deployments/StatefulSets: list for Tier-1/Tier-2 HA readiness checks.
			// DaemonSets: create/delete for the node-to-node DaemonSet probe; list to watch pod readiness.
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"get", "list"}},
			// ControllerRevisions: the creation time of a rolling StatefulSet's
			// update revision dates its rollout, which Tier-2 bounds.
			{APIGroups: []string{"apps"}, Resources: []string{"controllerrevisions"}, Verbs: []string{"get"}},
			{APIGroups: []string{"apps"}, Resources: []string{"daemonsets"}, Verbs: []string{"get", "list", "create", "delete"}},
			// Gateway API: the validator follows every NVCF route kind's
			// parentRefs to learn which Gateways are NVCF's, and reads those
			// Gateways' classes and the controllers that run them.
			{APIGroups: []string{"gateway.networking.k8s.io"}, Resources: []string{
				"gateways", "gatewayclasses", "httproutes", "grpcroutes", "tcproutes", "udproutes",
			}, Verbs: []string{"get", "list"}},
			{NonResourceURLs: []string{"/readyz", "/version", "/healthz"}, Verbs: []string{"get"}},
		},
	}
	// Create only, for the same reason as the ServiceAccount above. The name is
	// unique per run, so there are no stale rules from an older CLI to refresh
	// and nothing legitimate to overwrite.
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return true, fmt.Errorf("create cluster role: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: roleLabels},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      name,
			Namespace: clusterValidatorNamespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     name,
		},
	}
	// Create only, same reasoning as above.
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil {
		return true, fmt.Errorf("create cluster role binding: %w", err)
	}
	return true, nil
}

func clusterValidatorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       clusterValidatorAppLabel,
		"app.kubernetes.io/managed-by": clusterValidatorManagedBy,
		"app.kubernetes.io/component":  "preflight",
	}
}

// clusterValidatorRoleLabels are the managed labels plus the role, with no run
// ID. validatorRoleSelector matches on them, so a lookup for one role never
// returns the other role's objects in a ModeSingle run.
func clusterValidatorRoleLabels(role string) map[string]string {
	return clusterValidatorRunLabels(role, "", false)
}

// clusterValidatorRunLabels are the labels for one run's objects: the managed
// labels, the role, the run ID, and the preserve marker when the run was asked
// to keep its artifacts (which the orphan sweeper then keeps for
// preservedValidatorTTL instead of orphanValidatorRBACTTL).
func clusterValidatorRunLabels(role, runID string, preserve bool) map[string]string {
	l := clusterValidatorLabels()
	if role != "" {
		l[clusterValidatorRoleLabel] = role
	}
	if runID != "" {
		l[clusterValidatorRunLabel] = runID
	}
	if preserve {
		l[clusterValidatorPreserveLabel] = "true"
	}
	return l
}

// validatorConfigNameForRole returns the network-check ConfigMap name the Job
// should read. Only the control-plane role has one; the compute-plane role gets
// a sentinel that resolves to nothing so it cannot inherit the control-plane's
// reachability and enforcement config.
func validatorConfigNameForRole(role, runID string) string {
	if role == clusterValidatorControlPlaneRole {
		return clusterValidatorConfigRunName(runID)
	}
	return clusterValidatorNoConfigName
}

// clusterValidatorConfigLabels are the managed labels for the network-checks
// ConfigMap, plus the preserve marker when the run was asked to keep its
// artifacts. Without the marker the next run's orphan sweeper reclaims the
// ConfigMap once it is older than the TTL, and an operator re-running a Job
// they deliberately kept gets a validator that silently skips the configurable
// reachability and enforcement checks.
func clusterValidatorConfigLabels(runID string, preserve bool) map[string]string {
	return clusterValidatorRunLabels("", runID, preserve)
}

// clusterValidatorConfigRunName scopes the network-checks ConfigMap to one run.
// A shared name lets two overlapping commands overwrite each other's registry
// list, and lets the first to finish delete the config the other pod has not
// read yet.
func clusterValidatorConfigRunName(runID string) string {
	if runID == "" {
		return clusterValidatorConfigName
	}
	return clusterValidatorConfigName + "-" + runID
}

// sweepOrphanClusterValidatorRBAC removes validator RBAC left behind by a run
// that died before its deferred cleanup (SIGKILL, OOM, lost terminal).
//
// Needed because the names are random per run: a fixed name self-healed by
// being reused, these would accumulate. Only objects carrying our labels and
// older than the TTL are removed, so a concurrent run is never disturbed.
func sweepOrphanClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface, ttl time.Duration) {
	// Derived from the same map the objects are stamped with, so the selector
	// cannot drift from the labels. All three managed labels are required: the
	// component label is part of what identifies these as ours.
	opts := metav1.ListOptions{LabelSelector: labels.SelectorFromSet(clusterValidatorLabels()).String()}
	now := time.Now()

	// stale applies the TTL for the object's kind of run. A --no-cleanup run's
	// objects are kept longer, not forever: exempting them left a cluster-wide
	// ClusterRole and an NGC-key Secret behind permanently.
	stale := func(o metav1.Object) bool {
		limit := ttl
		if o.GetLabels()[clusterValidatorPreserveLabel] == "true" {
			limit = preservedValidatorTTL
		}
		ts := o.GetCreationTimestamp()
		return ts.Time.Before(now.Add(-limit))
	}

	// Each reclaimable test requires the generated name as well as the labels
	// and the age. Labels alone are public constants and can be copied onto
	// anything, and this deletes cluster-scoped objects with errors swallowed.
	// Network-check ConfigMaps and pull secrets carry their own name prefixes.
	reclaimableConfig := func(o metav1.Object) bool {
		return strings.HasPrefix(o.GetName(), clusterValidatorConfigName) && stale(o)
	}
	reclaimableSecret := func(o metav1.Object) bool {
		return strings.HasPrefix(o.GetName(), validatorPullSecretName) && stale(o)
	}
	// The bare legacy name is the fixed-name set released CLIs keep and reuse.
	// Its labels no longer match this selector, but exclude it by name too so
	// a released CLI's pod never loses its binding mid-run.
	reclaimable := func(o metav1.Object) bool {
		return strings.HasPrefix(o.GetName(), clusterValidatorName+"-") && stale(o)
	}

	if l, err := client.RbacV1().ClusterRoleBindings().List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimable(o) {
				_ = client.RbacV1().ClusterRoleBindings().Delete(ctx, o.Name, deleteExactly(o))
			}
		}
	}
	if l, err := client.RbacV1().ClusterRoles().List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimable(o) {
				_ = client.RbacV1().ClusterRoles().Delete(ctx, o.Name, deleteExactly(o))
			}
		}
	}
	// Network-check ConfigMaps too. Run-scoping the name removed the accidental
	// self-healing the fixed name gave us: a killed run used to leave exactly
	// one object that the next run overwrote, and now each leaves its own, so
	// under --wait they accumulate per poll. Same shape as the RBAC objects.
	cms := client.CoreV1().ConfigMaps(clusterValidatorNamespace)
	if l, err := cms.List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimableConfig(o) {
				_ = cms.Delete(ctx, o.Name, deleteExactly(o))
			}
		}
	}

	// Pull secrets too: the deferred sweep is suppressed whenever the pod may
	// still be running, and that is exactly the ImagePullBackOff path, so
	// without this arm a Secret holding $oauthtoken:$NGC_API_KEY is the one
	// object with no reclaim at all.
	secrets := client.CoreV1().Secrets(clusterValidatorNamespace)
	if l, err := secrets.List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimableSecret(o) {
				_ = secrets.Delete(ctx, o.Name, deleteExactly(o))
			}
		}
	}
	sa := client.CoreV1().ServiceAccounts(clusterValidatorNamespace)
	if l, err := sa.List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimable(o) {
				_ = sa.Delete(ctx, o.Name, deleteExactly(o))
			}
		}
	}

	// Jobs only need this for --no-cleanup runs: every other Job carries a
	// deadline and a TTL. A preserved Job has neither, so without this arm it
	// and its pod would stay forever. Background propagation removes the pod,
	// and the Job's ownerReferences take its Secret and ConfigMap with it.
	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	if l, err := jobs.List(ctx, opts); err == nil {
		propagation := metav1.DeletePropagationBackground
		for i := range l.Items {
			o := &l.Items[i]
			if o.Labels[clusterValidatorPreserveLabel] != "true" || !reclaimable(o) {
				continue
			}
			del := deleteExactly(o)
			del.PropagationPolicy = &propagation
			_ = jobs.Delete(ctx, o.Name, del)
		}
	}
}

// sweepClusterValidatorRBAC removes the SA, ClusterRole, and ClusterRoleBinding
// created by ensureClusterValidatorRBAC. Called after Job completion (when
// --no-cleanup is not set) to close the window where the elevated ClusterRole
// exists. Each run mints its own names, so nothing is reused. It returns what
// it could not remove, so the run can say how to remove it.
func sweepClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface, role, runID string) error {
	name := clusterValidatorRBACName(role, runID)

	// Delete by name, but only what we own: these are cluster-scoped, so an
	// operator-owned ClusterRole with a colliding name must never vanish.
	var errs []error
	if crb, err := client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{}); err == nil {
		if hasValidatorManagedLabels(crb.Labels) {
			errs = append(errs, ignoreNotFound(client.RbacV1().ClusterRoleBindings().Delete(ctx, name, deleteExactly(crb))))
		}
	} else {
		errs = append(errs, ignoreNotFound(err))
	}
	if cr, err := client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{}); err == nil {
		if hasValidatorManagedLabels(cr.Labels) {
			errs = append(errs, ignoreNotFound(client.RbacV1().ClusterRoles().Delete(ctx, name, deleteExactly(cr))))
		}
	} else {
		errs = append(errs, ignoreNotFound(err))
	}
	sa := client.CoreV1().ServiceAccounts(clusterValidatorNamespace)
	if acct, err := sa.Get(ctx, name, metav1.GetOptions{}); err == nil {
		if hasValidatorManagedLabels(acct.Labels) {
			errs = append(errs, ignoreNotFound(sa.Delete(ctx, name, deleteExactly(acct))))
		}
	} else {
		errs = append(errs, ignoreNotFound(err))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("removing validator RBAC %s: %w", name, err)
	}
	return nil
}

// ignoreNotFound drops a NotFound error: the object is already gone.
func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// sweepManagedPullSecrets removes this run's docker-registry secret in
// clusterValidatorNamespace (mirrored from another namespace or minted from
// NGC_API_KEY). Called after the Job terminates so NGC credentials don't
// persist across preflight runs. Other runs' secrets are left to their own
// sweeps or to sweepOrphanClusterValidatorRBAC.
//
// Operator-supplied secrets via --cluster-validator-pull-secret aren't
// labeled by us and are skipped by the selector.
func sweepManagedPullSecrets(ctx context.Context, client kubernetes.Interface, role, runID string) error {
	// The generated name is part of the ownership test, not just the labels,
	// which are public constants anything could carry.
	secrets := client.CoreV1().Secrets(clusterValidatorNamespace)
	l, err := secrets.List(ctx, metav1.ListOptions{LabelSelector: validatorRoleSelector(role)})
	if err != nil {
		return fmt.Errorf("listing validator pull secrets: %w", err)
	}
	for i := range l.Items {
		// Only this run's Secret. The name is unguessable and unique per run,
		// so a sweep can no longer take out a Secret another run selected.
		if l.Items[i].Name != validatorPullSecretRunName(role, runID) {
			continue
		}
		if err := ignoreNotFound(secrets.Delete(ctx, l.Items[i].Name, deleteExactly(&l.Items[i]))); err != nil {
			return fmt.Errorf("removing validator pull secret %s: %w", l.Items[i].Name, err)
		}
	}
	return nil
}

// validatorRoleSelector matches objects this CLI created for one validator role.
// Derived from clusterValidatorRoleLabels so it cannot drift from the labels
// actually stamped on the objects.
func validatorRoleSelector(role string) string {
	return labels.SelectorFromSet(clusterValidatorRoleLabels(role)).String()
}

// clusterValidatorControlPlaneRole is the role value passed as VALIDATOR_ROLE
// when running against the control-plane cluster. Matches nvca's RoleControlPlane
// without importing that package.
const clusterValidatorControlPlaneRole = "control-plane"

// clusterValidatorComputePlaneRole is the other VALIDATOR_ROLE value. Both are
// named so role-scoping logic reads the same on either side.
const clusterValidatorComputePlaneRole = "compute-plane"

// clusterValidatorConfigName is the default ConfigMap name the validator binary
// looks for when VALIDATOR_CONFIG_NAME is empty. Must stay in sync with
// defaultConfigMapName in nvca/cmd/cluster-validator/main.go.
const clusterValidatorConfigName = "cluster-validator-network-checks"

// clusterValidatorNoConfigName is a name no ConfigMap uses. VALIDATOR_CONFIG_NAME
// must be non-empty to suppress the validator's own default-name fallback, so
// "no config" has to be spelled as a name that resolves to nothing.
const clusterValidatorNoConfigName = "cluster-validator-no-config"

// clusterValidatorRoleLabel scopes Job selectors to a single validator role.
const clusterValidatorRoleLabel = "nvcf.nvidia.com/validator-role"

// clusterValidatorPreserveLabel marks objects an operator asked to keep with
// --no-cleanup. The orphan sweeper keeps them for preservedValidatorTTL instead
// of orphanValidatorRBACTTL: without the label a preserved run would be
// reclaimed after 30 minutes, and with no limit at all a kept ClusterRole and
// NGC-key Secret would stay forever.
const clusterValidatorPreserveLabel = "nvcf.nvidia.com/validator-preserve"

// clusterValidatorRBACName returns the ServiceAccount / ClusterRole /
// ClusterRoleBinding name for one run of one role.
//
// The runID is random per run, which is what makes the name safe to create.
// A predictable name can be squatted: the managed labels are three public
// constants, so anyone who can create a ServiceAccount in the probe namespace
// could pre-create one carrying them, and a label check alone would then adopt
// it and bind it to the validator ClusterRole. With an unguessable name there
// is no collision to adopt, so the resources are only ever created by us.
//
// Per-role naming also matters because ModeSplit runs both validators
// concurrently against contexts that can resolve to the same cluster.
func clusterValidatorRBACName(role, runID string) string {
	name := clusterValidatorName
	if role != "" {
		name += "-" + role
	}
	if runID != "" {
		name += "-" + runID
	}
	return name
}

// newValidatorRunID returns an unguessable suffix for this run's RBAC names.
// crypto/rand, not math/rand: a predictable value would defeat the point.
func newValidatorRunID() (string, error) {
	b := make([]byte, 5)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("generating run id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// controlPlaneValidatorEnforcementConfig follows the generated reachability
// section in the control-plane validator ConfigMap.
const controlPlaneValidatorEnforcementConfig = `enforcement:
  # Disabled for preflight. VALIDATOR_PREFLIGHT only suppresses the summary
  # write, so enforcement would still run: it creates netpol-validation
  # namespaces, server and client pods and NetworkPolicies, and pulls
  # busybox from Docker Hub. Doing that on a virgin or air-gapped cluster,
  # before anything is installed and with no flag to turn it off, is not
  # something a read-only readiness check should do.
  enabled: false
  testImage: busybox:1.36
  timeoutSeconds: 60
  critical: false
`

// ensureClusterValidatorConfig creates this run's network-check ConfigMap.
// Best-effort: the validator skips configurable checks when it is absent.
func ensureClusterValidatorConfig(ctx context.Context, client kubernetes.Interface, registries []RegistryEntry, runID string, preserve bool) error {
	content := buildControlPlaneValidatorConfig(registries)
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorConfigRunName(runID),
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorConfigLabels(runID, preserve),
		},
		Data: map[string]string{"config.yaml": content},
	}

	existing, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(ctx, clusterValidatorConfigRunName(runID), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get validator config ConfigMap: %w", err)
		}
		if _, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create validator config ConfigMap: %w", err)
		}
		return nil
	}

	// The name is per run, so an existing object was not created by this run.
	// Replace the content only when it carries our labels; never overwrite a
	// ConfigMap we do not own.
	if !hasValidatorManagedLabels(existing.Labels) {
		return fmt.Errorf(
			"refusing to overwrite ConfigMap %s/%s which is not managed by nvcf-cli",
			clusterValidatorNamespace, clusterValidatorConfigRunName(runID))
	}
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	if _, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update validator config ConfigMap: %w", err)
	}
	return nil
}

// buildControlPlaneValidatorConfig assembles the network-check ConfigMap YAML.
// The reachability endpoints are the registries the install pulls from, as
// EnumerateRegistries resolved them for the local credential check, and all of
// them are non-critical. The validator dials them from a pod with no proxy
// support, but nodes pull through the container runtime, often via an
// HTTPS_PROXY or a registry mirror that pods cannot use: a failed in-pod dial
// is worth a warning, not proof that the nodes cannot pull. With no
// registries the section is omitted.
func buildControlPlaneValidatorConfig(registries []RegistryEntry) string {
	var b strings.Builder
	for _, reg := range registries {
		host, port := parseRegistryHostPort(reg.Registry)
		if host == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("reachability:\n  endpoints:\n")
		}
		// Quote the values: they come from operator input and the stack
		// values. An unquoted "[" or embedded newline would make the whole
		// ConfigMap unparseable, and the validator then silently drops every
		// reachability and enforcement check.
		fmt.Fprintf(&b,
			"    - name: %q\n      host: %q\n      port: %d\n      protocol: tcp+tls\n      critical: false\n",
			reg.Registry, host, port)
	}
	return b.String() + controlPlaneValidatorEnforcementConfig
}

// parseRegistryHostPort splits a "host:port" string using net.SplitHostPort,
// which correctly handles IPv6 literals ([::1]:5000) and bare hostnames.
// Returns port 443 when no port is specified, the port is non-numeric, or
// the input has a trailing colon with no digit (e.g. "nvcr.io:").
func parseRegistryHostPort(s string) (host string, port int) {
	s = strings.TrimSpace(s)
	// Reject anything that is not a bare host[:port]. Callers interpolate the
	// result straight into a URL and into the validator ConfigMap, and an empty
	// host is the signal to skip the entry entirely.
	if !isBareRegistryHost(s) {
		return "", 0
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		// No port present (e.g. "nvcr.io"). Strip IPv6 brackets: the value
		// goes into the validator ConfigMap as a bare host, and the validator
		// brackets it itself with net.JoinHostPort, so "[fd00::1]" would be
		// dialled as "[[fd00::1]]:443" and reported unreachable.
		return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), 443
	}
	if p == "" {
		// Trailing colon with no port digit (e.g. "nvcr.io:") is the same typo.
		return "", 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		// An explicit but unparseable port is a typo, not a request for 443.
		// Silently probing a different endpoint than configured would report a
		// result for something the operator never asked about.
		return "", 0
	}
	return h, n
}

// buildClusterValidatorJob creates the validator Job. PullIfNotPresent reuses
// locally-imported images. VALIDATOR_PREFLIGHT=true skips the summary ConfigMap
// write. VALIDATOR_ROLE selects the check set (control-plane vs compute-plane).
func buildClusterValidatorJob(
	name, image, pullSecret, role, runID string, noCleanup bool, env map[string]string,
	tolerations ...corev1.Toleration,
) *batchv1.Job {
	backoff := int32(0)
	// Pod shape mirrors deployments/nvca-operator/templates/cronjob.yaml, which
	// runs this same image. Two producers of one pod spec now exist in two
	// languages, so they are kept deliberately in step: without the security
	// context the Job is rejected outright by a namespace enforcing the
	// PodSecurity "restricted" profile, and without the tolerations it never
	// schedules on a cluster whose nodes all carry the control-plane taint.
	// Either way waitForClusterValidatorJob burns its full budget and the run
	// leaks, so these are correctness, not hardening.
	runAsUser := int64(65534)
	runAsNonRoot := true
	readOnlyRoot := true
	allowPrivEsc := false
	podSpec := corev1.PodSpec{
		ServiceAccountName: clusterValidatorRBACName(role, runID),
		RestartPolicy:      corev1.RestartPolicyNever,
		Tolerations:        append(clusterValidatorTolerations(), tolerations...),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:  &runAsUser,
			RunAsGroup: &runAsUser,
			FSGroup:    &runAsUser,
		},
		Containers: []corev1.Container{{
			Name:            clusterValidatorContainer,
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Resources:       clusterValidatorResources(),
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             &runAsNonRoot,
				ReadOnlyRootFilesystem:   &readOnlyRoot,
				AllowPrivilegeEscalation: &allowPrivEsc,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Env: []corev1.EnvVar{
				{Name: "VALIDATOR_CONFIG_NAMESPACE", Value: clusterValidatorNamespace},
				// Name the ConfigMap explicitly for the control-plane role and
				// leave it unresolvable for the compute-plane role. An empty
				// value is NOT "no config": the validator falls back to its own
				// default name, so both roles would resolve the same ConfigMap
				// and the compute-plane preflight would silently run the
				// control-plane's active NetworkPolicy enforcement.
				{Name: "VALIDATOR_CONFIG_NAME", Value: validatorConfigNameForRole(role, runID)},
				{Name: "VALIDATOR_PREFLIGHT", Value: "true"},
				{Name: "VALIDATOR_ROLE", Value: role},
			},
		}},
	}
	podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, extraValidatorEnv(podSpec.Containers[0].Env, env)...)
	if pullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: pullSecret}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorRunLabels(role, runID, noCleanup),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: clusterValidatorRunLabels(role, runID, false)},
				Spec:       podSpec,
			},
		},
	}
	if !noCleanup {
		// Without a deadline a Job whose pod never finishes never becomes
		// terminal, so its TTL never fires. The deadline plus the TTL remove
		// the Job and its pod, and through ownerReferences its pull secret and
		// ConfigMap. The ServiceAccount, ClusterRole and binding are left to
		// the deferred sweeps, or to a later run's orphan sweep. Sized above
		// the runner's own timeout so it never truncates a wait that is still
		// making progress; the runner sets it from the time it has left. A
		// --no-cleanup Job gets neither, so the pod and its logs survive for
		// debugging until the operator removes them or a later check's orphan
		// sweep runs after preservedValidatorTTL.
		activeDeadline := validatorActiveDeadlineSeconds(clusterValidatorTimeout)
		job.Spec.ActiveDeadlineSeconds = &activeDeadline
		ttl := clusterValidatorTTLSeconds
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	return job
}

// Polls until Succeeded>0, Failed>0, or ctx expires. Each tick also checks
// for image-pull failure so a missing pull secret surfaces in ~30s instead
// of after the full 5m timeout.
func waitForClusterValidatorJob(ctx context.Context, client kubernetes.Interface, jobName string) (*batchv1.Job, error) {
	ticker := time.NewTicker(clusterValidatorPollInterval)
	defer ticker.Stop()
	getErrors := 0
	var pullFailingSince time.Time
	for {
		job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, jobName, metav1.GetOptions{})
		switch {
		case err != nil:
			// If the deadline fired between ticker and Get, surface the wait
			// wording the select branch would have used.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("waiting for job %s: %w", jobName, ctxErr)
			}
			// A missing Job or a permission error cannot change; anything else
			// is retried a few times before it decides the run.
			getErrors++
			if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) ||
				getErrors >= validatorGetErrorLimit {
				return nil, fmt.Errorf("get job %s: %w", jobName, err)
			}
		case jobFinished(job):
			return job, nil
		default:
			getErrors = 0
			reason, msg := podPullFailureReason(ctx, client, jobName)
			switch {
			case reason == "":
				pullFailingSince = time.Time{}
			case reason == "InvalidImageName" || permanentPullFailure(msg):
				// A malformed reference never pulls, nor does an image the
				// registry does not have or will not serve to these credentials.
				return job, &validatorImagePullError{reason: reason, message: msg}
			case pullFailingSince.IsZero():
				pullFailingSince = time.Now()
			case time.Since(pullFailingSince) >= validatorPullFailureGrace:
				return job, &validatorImagePullError{reason: reason, message: msg}
			}
		}
		select {
		case <-ctx.Done():
			return job, fmt.Errorf("waiting for job %s: %w", jobName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// permanentPullFailures are in the kubelet's message for a pull that no retry
// fixes: the image is not in the registry, or the credentials are refused.
var permanentPullFailures = []string{
	"not found", "manifest unknown", "unauthorized", "authentication required", "denied",
}

// permanentPullFailure reports whether a pull error message says retrying
// cannot help.
func permanentPullFailure(message string) bool {
	message = strings.ToLower(message)
	for _, m := range permanentPullFailures {
		if strings.Contains(message, m) {
			return true
		}
	}
	return false
}

// Returns ("", "") when no pull failure is observed; callers treat that
// as "keep waiting".
func podPullFailureReason(ctx context.Context, client kubernetes.Interface, jobName string) (reason, message string) {
	podName, _ := podNameForClusterValidatorJob(ctx, client, jobName)
	if podName == "" {
		return "", ""
	}
	pod, err := client.CoreV1().Pods(clusterValidatorNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			if _, ok := pullFailureReasons[cs.State.Waiting.Reason]; ok {
				return cs.State.Waiting.Reason, cs.State.Waiting.Message
			}
		}
	}
	// FailedToRetrieveImagePullSecret can surface on Pod conditions before
	// any container status is reported.
	for _, cond := range pod.Status.Conditions {
		if cond.Reason == "" {
			continue
		}
		if _, ok := pullFailureReasons[cond.Reason]; ok {
			return cond.Reason, cond.Message
		}
	}
	return "", ""
}

// Returns ("", err) when the log stream cannot be opened; callers store
// whatever they got and surface the underlying waitErr.
func fetchClusterValidatorLogs(ctx context.Context, client kubernetes.Interface, jobName string) (string, error) {
	podName, err := podNameForClusterValidatorJob(ctx, client, jobName)
	if err != nil || podName == "" {
		return "", err
	}
	req := client.CoreV1().Pods(clusterValidatorNamespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: clusterValidatorContainer,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open log stream for %s: %w", podName, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read logs for %s: %w", podName, err)
	}
	return string(b), nil
}

// Filters on the job-name label rather than OwnerReferences so the helper
// works against fake clientsets that don't populate OwnerReferences.
func podNameForClusterValidatorJob(ctx context.Context, client kubernetes.Interface, jobName string) (string, error) {
	pods, err := client.CoreV1().Pods(clusterValidatorNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", fmt.Errorf("list pods for job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", nil
	}
	return pods.Items[0].Name, nil
}

// containerTermination returns the validator container's exit code, -1 when
// it cannot be read or the container never ended by itself, and the reason
// the container or its pod ended when that says more than the code: an OOM
// kill or an eviction. Informational only; the canonical pass/fail signal is
// the Job's status.
func containerTermination(ctx context.Context, client kubernetes.Interface, jobName string) (int32, string) {
	podName, _ := podNameForClusterValidatorJob(ctx, client, jobName)
	if podName == "" {
		return -1, ""
	}
	pod, err := client.CoreV1().Pods(clusterValidatorNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return -1, ""
	}
	code, reason := int32(-1), ""
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == clusterValidatorContainer && cs.State.Terminated != nil {
			code, reason = cs.State.Terminated.ExitCode, cs.State.Terminated.Reason
		}
	}
	if reason == "" || reason == "Error" || reason == "Completed" {
		reason = pod.Status.Reason
	}
	return code, reason
}

// Idempotent: running twice yields the same output.
func cleanValidatorOutput(raw string) string {
	if raw == "" {
		return ""
	}
	s := ansiEscapeRE.ReplaceAllString(raw, "")
	s = boxDrawingRE.ReplaceAllString(s, "")
	s = blankLineRE.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}

// Pinned to the context the Job was created in: in ModeSplit the two roles
// run against different clusters, so a bare kubectl command resolves to
// whichever context is current and the hint reports "job not found".
func kubectlLogsHint(kubeContext, jobName string) string {
	if jobName == "" {
		return ""
	}
	return fmt.Sprintf("kubectl%s logs -n %s job/%s --tail=-1",
		kubectlContextArg(kubeContext), clusterValidatorNamespace, jobName)
}

// sweepClusterValidatorConfig removes the network-checks ConfigMap this run
// created. The name is scoped to this run, and the managed labels are checked
// as well, so a same-named object belonging to anyone else is left alone.
// It returns what it could not remove, as the other sweeps do.
func sweepClusterValidatorConfig(ctx context.Context, client kubernetes.Interface, runID string) error {
	cms := client.CoreV1().ConfigMaps(clusterValidatorNamespace)
	name := clusterValidatorConfigRunName(runID)
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if err = ignoreNotFound(err); err != nil {
			return fmt.Errorf("reading validator ConfigMap %s: %w", name, err)
		}
		return nil
	}
	if !hasValidatorManagedLabels(cm.Labels) {
		return nil
	}
	if err := ignoreNotFound(cms.Delete(ctx, name, deleteExactly(cm))); err != nil {
		return fmt.Errorf("removing validator ConfigMap %s: %w", name, err)
	}
	return nil
}

// clusterValidatorTolerations mirrors the chart's tolerations so the Job can
// schedule on a cluster whose nodes all carry the control-plane taint, which
// is the normal shape for a dedicated control plane.
func clusterValidatorTolerations() []corev1.Toleration {
	return []corev1.Toleration{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "node-role.kubernetes.io/master", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
	}
}

// clusterValidatorResources mirrors the chart's defaults. Without requests the
// Job is rejected outright by a namespace carrying a LimitRange or a quota.
func clusterValidatorResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}
