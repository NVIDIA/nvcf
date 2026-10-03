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
	// orphanValidatorRBACTTL is the minimum age, on the apiserver's clock,
	// before leftover validator objects are reclaimed. It must exceed the
	// longest a run or its pod can live, so a live run is never hit.
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

// validatorTeardownBudget is the longest an interrupted validator spends
// removing its node-to-node probe: the DaemonSet, the pods and the namespace,
// one after another, each with its own 20s budget.
const validatorTeardownBudget = 3 * 20 * time.Second

// clusterValidatorTerminationGrace is the validator pod's termination grace
// period, the same as the chart's nvcaop.clusterValidatorJobSpec. It leaves
// room past validatorTeardownBudget, so the kubelet does not kill the pod
// before the probe namespace is deleted.
const clusterValidatorTerminationGrace = 120 * time.Second

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
	// the hosts the local credential check probes, each as a warning only.
	// Ignored for the compute-plane role.
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
	// Notes say what the run did that the operator should know about, such
	// as copying a pull credential into the validator namespace.
	Notes []string
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
func runValidatorJob(
	ctx context.Context, client kubernetes.Interface, p ClusterValidatorParams,
) (res ClusterValidatorResult) {
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
	// Everything this run's Creates returned. Cleanup deletes only these,
	// pinned to the UID each Create returned: a name this run failed to claim
	// belongs to someone else, whatever labels it carries.
	created := runObjects{}

	// sweep runs one deferred delete of this run's objects of the given kinds
	// on a fresh, bounded context, and only once no pod of this run can still
	// need them. --no-cleanup keeps everything. A delete that fails leaves
	// objects behind, so the result carries its error and the caller prints
	// the removal command.
	sweep := func(kinds ...string) {
		if noCleanup || podMayBeRunning {
			return
		}
		cctx, cancel := cleanupContext()
		defer cancel()
		if err := deleteCreated(cctx, client, created, kinds...); err != nil {
			res.SweepErr = errors.Join(res.SweepErr, err)
			res.Created = true
		}
	}

	// Register the cleanup before the bootstrap, not after. ensureClusterValidatorRBAC
	// creates its objects in sequence and returns on the first failure, so a
	// kubeconfig that can create a ServiceAccount but not a cluster-scoped
	// ClusterRole would otherwise abandon the ServiceAccount on every attempt.
	// The names are per-run, so nothing self-heals by reuse: under --wait that
	// leaks one object per poll.
	defer sweep(validatorRBACKinds...)
	rbacErr := ensureClusterValidatorRBAC(vctx, client, role, runID, noCleanup, created)

	// Reclaim leftovers from dead runs. Their age is measured on the
	// apiserver's clock, read from what this run just created, so a skewed
	// workstation clock cannot make an overlapping run's objects look stale.
	// This run's objects and an explicitly named pull Secret are never taken.
	if now, ok := created.serverTime(); ok {
		sweepOrphanClusterValidatorRBAC(vctx, client, now, orphanValidatorRBACTTL, runID, pullSecret)
	}
	if rbacErr != nil {
		// RunID lets --no-cleanup print the command that removes whatever the
		// failed bootstrap already created.
		return ClusterValidatorResult{
			RunID: runID, Created: len(created) > 0, Err: fmt.Errorf("bootstrapping validator RBAC: %w", rbacErr),
		}
	}

	// The pull secret is resolved only once the run can start a Job: minting
	// it before the bootstrap wrote the NGC key into the namespace on every
	// run whose kubeconfig could not create the RBAC. A resolver error does
	// not stop the run: the image may be public. It is kept, so a pull that
	// then fails names the step that failed rather than only the registry's
	// "unauthorized".
	resolved, pullSecretNote, pullSecretErr := resolveValidatorPullSecret(
		ctx, client, pullSecret, image, role, runID, noCleanup, created)
	if pullSecretErr == nil {
		pullSecret = resolved
	}
	if pullSecretNote != "" {
		defer func() { res.Notes = append(res.Notes, pullSecretNote) }()
	}
	// Sweep only the Secret this run created, and only once the pod can no
	// longer need it. Sweeping unconditionally deletes the Secret out from
	// under a pod that is still retrying its pull, which the kubelet then
	// reports as FailedToRetrieveImagePullSecret. An operator-supplied or
	// adopted Secret was not created by this run and is never touched.
	defer sweep(kindSecret)

	// For the control-plane role, create a ConfigMap with reachability
	// endpoints and enforcement config so the validator runs its configurable
	// checks. Best-effort: on a failure the Job reads no ConfigMap, and the
	// validator skips the configurable checks.
	var configNote string
	configName := clusterValidatorNoConfigName
	if role == clusterValidatorControlPlaneRole {
		// Same gating as the other sweeps: keep it when the pod may still
		// read it, or when the operator asked to keep the run's artifacts.
		defer sweep(kindConfigMap)
		if err := ensureClusterValidatorConfig(vctx, client, p.Registries, role, runID, noCleanup, created); err != nil {
			// The validator skips configurable reachability checks silently
			// unless this note reaches the transcript.
			configNote = fmt.Sprintf("note: validator config not applied (%v); reachability checks may be skipped", err)
		} else {
			// Only a ConfigMap this run created is wired in. One that already
			// held the name was put there by someone else, and pointing the
			// Job at it would run their endpoints and enforcement settings.
			configName = clusterValidatorConfigRunName(runID)
		}
	}

	// No sweep of earlier Jobs: every Job now carries a deadline and a TTL,
	// so a role-wide sweep could only ever hit an overlapping run's live Job.
	// Named like the RBAC, per role and run, so two runs never share a name.
	jobName := clusterValidatorRBACName(role, runID)
	spec := buildClusterValidatorJob(jobName, image, pullSecret, role, runID, configName, noCleanup, p.Env,
		p.Tolerations...)
	// Both counted from now, not from the start of the run: the sweep, the
	// pull-secret scan and the bootstrap have used some of its timeout.
	left := timeLeft(vctx)
	setValidatorTimeout(spec, left)
	if spec.Spec.ActiveDeadlineSeconds != nil {
		deadline := validatorActiveDeadlineSeconds(left)
		spec.Spec.ActiveDeadlineSeconds = &deadline
	}
	job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Create(vctx, spec, metav1.CreateOptions{})
	if err != nil {
		// A create cut off by an interrupt or a dropped connection may still
		// have been applied, and a Job left behind would run with its RBAC
		// swept. AlreadyExists means the name was someone else's, which is
		// never touched; any other Job under the name is acted on only if it
		// carries this run's ID. Without --no-cleanup it is stopped, and when
		// its pod may still be running its objects are kept and the result
		// says how to remove them. Under --no-cleanup it is kept, but
		// suspended: nothing follows it, and with no deadline a pod that
		// cannot pull would retry forever.
		res := ClusterValidatorResult{RunID: runID, Created: true, Err: fmt.Errorf("creating validator Job: %w", err)}
		switch {
		case apierrors.IsAlreadyExists(err):
		case noCleanup:
			if serr := suspendValidatorJob(client, jobName, runID, created); serr != nil && !apierrors.IsNotFound(serr) {
				res.Err = fmt.Errorf("%w; suspending the kept Job failed: %v", res.Err, serr)
			}
		default:
			podMayBeRunning = !stopValidatorJob(client, jobName, runID, created)
			res.LeftBehind = podMayBeRunning
		}
		return res
	}
	created.add(kindJob, job)
	podMayBeRunning = true

	// The Job owns this run's pull secret and ConfigMap, so they go when the
	// Job goes, even on paths where the deferred sweeps are suppressed: its
	// deadline and TTL remove it. The ServiceAccount is deliberately not
	// owned: the ClusterRole and binding are cluster-scoped and cannot be,
	// and deleting the account alone would leave a binding that anyone able
	// to recreate that ServiceAccount name could inherit.
	// Not under --no-cleanup: a kept Secret or ConfigMap owned by the kept Job
	// would be garbage-collected the moment an operator re-runs that Job by
	// deleting and recreating it. They stay unowned, and a later check's
	// orphan sweep reclaims them once preservedValidatorTTL has passed. On a
	// fresh context: an interrupt right after the create must not leave them
	// unowned.
	if !noCleanup {
		octx, ocancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
		ownByJob(octx, client, job, created)
		ocancel()
	}

	final, waitErr := waitForClusterValidatorJob(vctx, client, jobName)
	// The run's own context has ended: the operator interrupted it, or the
	// check's time budget ran out. Nothing will wait for a later result, so
	// the Job is stopped now rather than left to its deadline.
	abandoned := waitErr != nil && ctx.Err() != nil
	var pullErr *validatorImagePullError
	isPullErr := errors.As(waitErr, &pullErr)
	if isPullErr && pullSecretErr != nil {
		waitErr = fmt.Errorf("%w; the run's pull secret was not created: %w", waitErr, pullSecretErr)
	}
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
			podMayBeRunning = !stopValidatorJob(client, jobName, runID, created)
			stopped = true
		case pullFailing || validatorPodPullFailing(client, jobName):
			suspended, suspendErr = true, suspendValidatorJob(client, jobName, runID, created)
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

// validatorTimeoutEnv bounds the validator's checks. The launcher owns it, so
// extraValidatorEnv never overrides it: set below the time this run waits, it
// has the validator end with its own verdict, naming the checks it could not
// finish, instead of being cut off without one.
const validatorTimeoutEnv = "VALIDATOR_TIMEOUT"

// validatorSummaryMargin is the part of the run's wait left to the validator
// after its own timeout, to print its summary and exit.
const validatorSummaryMargin = 30 * time.Second

// validatorRunTimeout is VALIDATOR_TIMEOUT, in seconds, for a run with left to
// wait: validatorSummaryMargin less, or half of left when that is short.
func validatorRunTimeout(left time.Duration) string {
	d := max(left-validatorSummaryMargin, left/2)
	return strconv.FormatInt(max(int64(d/time.Second), 1), 10) + "s"
}

// setValidatorTimeout sets the Job's VALIDATOR_TIMEOUT for a run with left to
// wait.
func setValidatorTimeout(job *batchv1.Job, left time.Duration) {
	env := job.Spec.Template.Spec.Containers[0].Env
	for i := range env {
		if env[i].Name == validatorTimeoutEnv {
			env[i].Value = validatorRunTimeout(left)
		}
	}
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

// Kinds of object one run creates, as runObjects records them.
const (
	kindServiceAccount     = "ServiceAccount"
	kindClusterRole        = "ClusterRole"
	kindClusterRoleBinding = "ClusterRoleBinding"
	kindRole               = "Role"
	kindRoleBinding        = "RoleBinding"
	kindSecret             = "Secret"
	kindConfigMap          = "ConfigMap"
	kindJob                = "Job"
)

// validatorRBACKinds are the RBAC kinds in the order they are removed: the
// bindings first, so the grants go before the roles and the account they name.
var validatorRBACKinds = []string{
	kindClusterRoleBinding, kindRoleBinding, kindClusterRole, kindRole, kindServiceAccount,
}

// runObjects records each object one run created, by kind, as its Create
// returned it. It is the ownership test for every delete the run makes: the
// managed labels are public constants anyone can copy, and a name the run
// failed to claim with AlreadyExists holds someone else's object.
type runObjects map[string]metav1.Object

// add records o under kind. A nil runObjects records nothing.
func (r runObjects) add(kind string, o metav1.Object) {
	if r != nil && o != nil {
		r[kind] = o
	}
}

// serverTime is the apiserver's clock as of this run's latest Create, from the
// creationTimestamps the Creates returned. ok is false until one has.
func (r runObjects) serverTime() (now time.Time, ok bool) {
	for _, o := range r {
		if ts := o.GetCreationTimestamp().Time; ts.After(now) {
			now = ts
		}
	}
	return now, !now.IsZero()
}

// deleteCreated deletes this run's objects of the given kinds, in order, each
// pinned to the UID its Create returned. It returns what it could not remove,
// so the run can say how to remove it. An object already gone is not an
// error, nor is a UID mismatch: the name then holds someone else's object,
// and this run's is gone.
func deleteCreated(ctx context.Context, client kubernetes.Interface, created runObjects, kinds ...string) error {
	var errs []error
	for _, kind := range kinds {
		o, ok := created[kind]
		if !ok {
			continue
		}
		name, opts := o.GetName(), deleteExactly(o)
		var err error
		switch kind {
		case kindServiceAccount:
			err = client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Delete(ctx, name, opts)
		case kindClusterRole:
			err = client.RbacV1().ClusterRoles().Delete(ctx, name, opts)
		case kindClusterRoleBinding:
			err = client.RbacV1().ClusterRoleBindings().Delete(ctx, name, opts)
		case kindRole:
			err = client.RbacV1().Roles(clusterValidatorNamespace).Delete(ctx, name, opts)
		case kindRoleBinding:
			err = client.RbacV1().RoleBindings(clusterValidatorNamespace).Delete(ctx, name, opts)
		case kindSecret:
			err = client.CoreV1().Secrets(clusterValidatorNamespace).Delete(ctx, name, opts)
		case kindConfigMap:
			err = client.CoreV1().ConfigMaps(clusterValidatorNamespace).Delete(ctx, name, opts)
		}
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			errs = append(errs, fmt.Errorf("removing validator %s %s: %w", kind, name, err))
		}
	}
	return errors.Join(errs...)
}

// ownByJob sets the Job as the controller-less owner of the pull Secret and
// ConfigMap this run created, so garbage collection removes them with it.
// Best-effort: a failure only means they wait for the deferred or orphan sweep.
func ownByJob(ctx context.Context, client kubernetes.Interface, job *batchv1.Job, created runObjects) {
	if job == nil || job.UID == "" {
		return
	}
	for _, kind := range []string{kindSecret, kindConfigMap} {
		o, ok := created[kind]
		if !ok {
			continue
		}
		// The UID is immutable, so carrying it makes the apiserver reject the
		// patch on an object that replaced this run's under the same name,
		// instead of making it a dependent the Job's deletion then removes.
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
			"uid": o.GetUID(),
			"ownerReferences": []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
			}},
		}})
		if err != nil {
			return
		}
		switch kind {
		case kindSecret:
			_, _ = client.CoreV1().Secrets(clusterValidatorNamespace).Patch(
				ctx, o.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
		case kindConfigMap:
			_, _ = client.CoreV1().ConfigMaps(clusterValidatorNamespace).Patch(
				ctx, o.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
		}
	}
}

// thisRunsValidatorJob returns this run's Job: the one its Create returned,
// or, when a Create errored but may still have been applied, the Job under
// name read back, only if it carries this run's ID, which is unguessable. It
// returns nil with the read's error when the Job could not be read, and nil
// with no error when the name holds another run's Job.
func thisRunsValidatorJob(
	ctx context.Context, client kubernetes.Interface, name, runID string, created runObjects,
) (metav1.Object, error) {
	if target, ok := created[kindJob]; ok {
		return target, nil
	}
	job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if runID == "" || job.Labels[clusterValidatorRunLabel] != runID || !hasValidatorManagedLabels(job.Labels) {
		return nil, nil
	}
	return job, nil
}

// deleteValidatorJob removes this run's Job and its pod, pinned to the Job's
// UID (see thisRunsValidatorJob). It reports whether the Job may be this
// run's: false only when there is none, or it is another's.
func deleteValidatorJob(
	ctx context.Context, client kubernetes.Interface, name, runID string, created runObjects,
) (mayBeOurs bool) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	target, err := thisRunsValidatorJob(dctx, client, name, runID, created)
	if target == nil {
		// Unreadable is unknown, so its pod may be this run's.
		return err != nil && !apierrors.IsNotFound(err)
	}
	// Foreground: the Job is removed only after its pod, so the pod gets its
	// SIGTERM and grace period while its RBAC still exists.
	propagation := metav1.DeletePropagationForeground
	opts := deleteExactly(target)
	opts.PropagationPolicy = &propagation
	_ = jobs.Delete(dctx, name, opts)
	return true
}

// Production values of the waits TestMain shortens, so tests can pin them.
const (
	defaultValidatorStopTimeout      = clusterValidatorTerminationGrace + 15*time.Second
	defaultValidatorDeadlineGrace    = validatorDeadlineOffset + clusterValidatorTerminationGrace + 30*time.Second
	defaultValidatorPullFailureGrace = 90 * time.Second
)

// Vars (not consts) so tests can shorten them.
var (
	// validatorStopTimeout bounds the wait for a stopped Job's pod to end.
	// It covers the pod's termination grace period.
	validatorStopTimeout = defaultValidatorStopTimeout
	// validatorDeadlineGrace bounds the wait, after this run's own timeout,
	// for the Job's active deadline (validatorDeadlineOffset later) to end
	// the pod, including its termination grace period, and for the Job to
	// record it.
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

// stopValidatorJob deletes this run's Job and reports whether its pods ended
// within validatorStopTimeout.
func stopValidatorJob(client kubernetes.Interface, name, runID string, created runObjects) bool {
	if !deleteValidatorJob(context.Background(), client, name, runID, created) {
		// Another run's pods under this name are not this run's to wait for.
		return true
	}
	return waitValidatorPodsDone(client, name, validatorStopTimeout)
}

// suspendValidatorJob stops this run's kept Job from creating or retrying its
// pod. The patch carries the Job's UID, so it fails on any other object that
// holds the name. A Job under the name that is not this run's is left alone.
func suspendValidatorJob(client kubernetes.Interface, name, runID string, created runObjects) error {
	ctx, cancel := context.WithTimeout(context.Background(), clusterValidatorLogFetchTimeout)
	defer cancel()
	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	target, err := thisRunsValidatorJob(ctx, client, name, runID, created)
	if target == nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": target.GetUID()},
		"spec":     map[string]any{"suspend": true},
	})
	if err != nil {
		return err
	}
	_, err = jobs.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
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

// ensureClusterValidatorRBAC creates this run's ServiceAccount, ClusterRole,
// ClusterRoleBinding, and the Role and RoleBinding that let it read its config
// ConfigMap, recording each in created. Create only: the names are unique per
// run, so an object that already exists was not made by this run.
func ensureClusterValidatorRBAC(
	ctx context.Context, client kubernetes.Interface, role, runID string, preserve bool, created runObjects,
) error {
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
	gotSA, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create service account: %w", err)
	}
	created.add(kindServiceAccount, gotSA)

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: roleLabels},
		Rules:      validatorClusterRules(role),
	}
	// Create only, for the same reason as the ServiceAccount above. The name is
	// unique per run, so there are no stale rules from an older CLI to refresh
	// and nothing legitimate to overwrite.
	gotCR, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create cluster role: %w", err)
	}
	created.add(kindClusterRole, gotCR)

	subjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: clusterValidatorNamespace}}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: roleLabels},
		Subjects:   subjects,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
	}
	gotCRB, err := client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create cluster role binding: %w", err)
	}
	created.add(kindClusterRoleBinding, gotCRB)

	// The config ConfigMap is read by name in this namespace, so its grant is
	// namespaced and limited to the names this run's Job can be pointed at,
	// instead of every ConfigMap in the cluster.
	r := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace, Labels: roleLabels},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"},
			ResourceNames: validatorConfigNames(role, runID),
		}},
	}
	gotRole, err := client.RbacV1().Roles(clusterValidatorNamespace).Create(ctx, r, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	created.add(kindRole, gotRole)

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace, Labels: roleLabels},
		Subjects:   subjects,
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
	}
	gotRB, err := client.RbacV1().RoleBindings(clusterValidatorNamespace).Create(ctx, rb, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create role binding: %w", err)
	}
	created.add(kindRoleBinding, gotRB)
	return nil
}

// validatorConfigNames are the ConfigMap names one run's Job can be pointed
// at: the no-config sentinel, and for the control-plane role the run's own.
func validatorConfigNames(role, runID string) []string {
	if role == clusterValidatorControlPlaneRole {
		return []string{clusterValidatorConfigRunName(runID), clusterValidatorNoConfigName}
	}
	return []string{clusterValidatorNoConfigName}
}

// validatorClusterRules are one role's cluster-wide grants: exactly the calls
// the validator makes for that role when this CLI launches it, which is with
// VALIDATOR_PREFLIGHT set, enforcement disabled and no networkPolicies pairs.
// testdata/validator_api_calls.yaml lists each call with the function that
// makes it, and a test holds these rules to that list. Discovery and the
// unauthenticated in-cluster /readyz dial need no grant.
func validatorClusterRules(role string) []rbacv1.PolicyRule {
	shared := []rbacv1.PolicyRule{
		// Admission webhook inventory, for both roles.
		{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{
			"mutatingwebhookconfigurations", "validatingwebhookconfigurations",
		}, Verbs: []string{"list"}},
		// CNI detection lists NetworkPolicies. The enforcement test, which
		// writes them, is disabled in the CLI's ConfigMap.
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"list"}},
		{NonResourceURLs: []string{"/readyz", "/version"}, Verbs: []string{"get"}},
	}
	if role != clusterValidatorControlPlaneRole {
		return append([]rbacv1.PolicyRule{
			// Read-only, except that every run deletes the netpol-validation
			// and node-to-node probe namespaces earlier runs left behind, and
			// force-deletes the probe pods that keep one stuck Terminating.
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"list"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "delete"}},
			// SMB CSI driver and its version, read from its Deployment.
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"csidrivers"}, Verbs: []string{"get"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list"}},
		}, shared...)
	}
	return append([]rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes", "services", "events"}, Verbs: []string{"list"}},
		// The node-to-node probe creates and deletes its own namespace,
		// DaemonSet and checker pods, and sweeps those of dead runs.
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"get", "list", "create",
			"delete"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list", "create", "delete"}},
		{APIGroups: []string{"apps"}, Resources: []string{"daemonsets"}, Verbs: []string{"list", "create", "delete"}},
		// The probe allows its own traffic in its namespace, so a default-deny
		// policy does not read as an overlay fault. The namespace delete
		// removes it.
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"create"}},
		// The default class is found by a list; the class the stack names
		// (NVCF_STORAGE_CLASS) is read by name.
		{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: []string{"get", "list"}},
		// Tier-1 and Tier-2 readiness, and the Envoy proxies behind the NVCF
		// Gateways.
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"list"}},
		// The creation time of a rolling StatefulSet's update revision dates
		// its rollout, which Tier-2 bounds.
		{APIGroups: []string{"apps"}, Resources: []string{"controllerrevisions"}, Verbs: []string{"get"}},
		// The validator follows every NVCF route kind's parentRefs to learn
		// which Gateways are NVCF's, and lists the GatewayClasses to tell
		// which of those Gateways Envoy Gateway runs.
		{APIGroups: []string{"gateway.networking.k8s.io"}, Resources: []string{
			"gateways", "gatewayclasses", "httproutes", "grpcroutes", "tcproutes", "udproutes",
		}, Verbs: []string{"list"}},
	}, shared...)
}

func clusterValidatorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       clusterValidatorAppLabel,
		"app.kubernetes.io/managed-by": clusterValidatorManagedBy,
		"app.kubernetes.io/component":  "preflight",
	}
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

// clusterValidatorConfigLabels are the labels for one run's network-checks
// ConfigMap, the same as for the run's other objects, including the preserve
// marker when the run was asked to keep its artifacts. Without the marker the
// next run's orphan sweeper reclaims the ConfigMap once it is older than the
// TTL, and an operator re-running a Job they deliberately kept gets a
// validator that silently skips the configurable reachability checks.
func clusterValidatorConfigLabels(role, runID string, preserve bool) map[string]string {
	return clusterValidatorRunLabels(role, runID, preserve)
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

// sweepOrphanClusterValidatorRBAC removes validator objects left behind by a
// run that died before its deferred cleanup (SIGKILL, OOM, lost terminal).
//
// Needed because the names are random per run: a fixed name self-healed by
// being reused, these would accumulate. Only objects carrying our labels and
// older than the TTL are removed, so a concurrent run is never disturbed. Age
// is measured against now, the apiserver's clock, never the local one: a
// workstation running ahead would otherwise see an overlapping run's fresh
// objects as stale. Objects of runID, the calling run, are never taken, nor a
// Secret named pullSecret, which the operator passed explicitly.
func sweepOrphanClusterValidatorRBAC(
	ctx context.Context, client kubernetes.Interface, now time.Time, ttl time.Duration, runID, pullSecret string,
) {
	// Derived from the same map the objects are stamped with, so the selector
	// cannot drift from the labels. All three managed labels are required: the
	// component label is part of what identifies these as ours.
	opts := metav1.ListOptions{LabelSelector: labels.SelectorFromSet(clusterValidatorLabels()).String()}

	// stale applies the TTL for the object's kind of run. A --no-cleanup run's
	// objects are kept longer, not forever: exempting them left a cluster-wide
	// ClusterRole and an NGC-key Secret behind permanently.
	stale := func(o metav1.Object) bool {
		if runID != "" && o.GetLabels()[clusterValidatorRunLabel] == runID {
			return false
		}
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
		return o.GetName() != pullSecret && strings.HasPrefix(o.GetName(), validatorPullSecretName) && stale(o)
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
	rbs := client.RbacV1().RoleBindings(clusterValidatorNamespace)
	if l, err := rbs.List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimable(o) {
				_ = rbs.Delete(ctx, o.Name, deleteExactly(o))
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
	roles := client.RbacV1().Roles(clusterValidatorNamespace)
	if l, err := roles.List(ctx, opts); err == nil {
		for i := range l.Items {
			if o := &l.Items[i]; reclaimable(o) {
				_ = roles.Delete(ctx, o.Name, deleteExactly(o))
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
	// and its pod would stay forever. Background propagation removes the pod.
	// A preserved run's Secret and ConfigMap are not owned by its Job, so the
	// arms above reclaim them on the same TTL.
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

// clusterValidatorNoConfigName is a name no ConfigMap can have. VALIDATOR_CONFIG_NAME
// must be non-empty to suppress the validator's own default-name fallback, so
// "no config" has to be spelled as a name that resolves to nothing. The
// uppercase letters make it an invalid object name, so the apiserver refuses
// to create a ConfigMap under it: anyone able to create ConfigMaps here could
// otherwise feed every run critical endpoints and an enabled enforcement test.
const clusterValidatorNoConfigName = "cluster-validator-NO-CONFIG"

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
//
// No testImage: the validator also reads it for the node-to-node overlay
// probe, so a value here would override the validator's own default whenever
// --cluster-validator-probe-image is not set.
const controlPlaneValidatorEnforcementConfig = `enforcement:
  # Disabled for preflight. VALIDATOR_PREFLIGHT only suppresses the summary
  # write, so enforcement would still run: it creates netpol-validation
  # namespaces, server and client pods and NetworkPolicies, and pulls
  # busybox from Docker Hub. Doing that on a virgin or air-gapped cluster,
  # before anything is installed and with no flag to turn it off, is not
  # something a read-only readiness check should do.
  enabled: false
`

// ensureClusterValidatorConfig creates this run's network-check ConfigMap and
// records it in created. Create only: the name carries this run's ID, so an
// object already holding it was put there by someone else, and adopting it,
// with whatever labels it carries, would hand the validator their endpoints
// and enforcement settings and then delete their object.
func ensureClusterValidatorConfig(
	ctx context.Context, client kubernetes.Interface, registries []RegistryEntry, role, runID string, preserve bool,
	created runObjects,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorConfigRunName(runID),
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorConfigLabels(role, runID, preserve),
		},
		Data: map[string]string{"config.yaml": buildControlPlaneValidatorConfig(registries)},
	}
	got, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create validator config ConfigMap: %w", err)
	}
	created.add(kindConfigMap, got)
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
	listed := map[string]bool{}
	for _, reg := range registries {
		host, port := parseRegistryHostPort(reg.Registry)
		// A registry probed for two repository scopes is one endpoint.
		if host == "" || listed[reg.Registry] {
			continue
		}
		listed[reg.Registry] = true
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
// Returns port 443 when no port is specified. Returns an empty host for
// anything that is not a bare host[:port], including a port that is not a
// number from 1 to 65535 and a trailing colon with no port ("nvcr.io:").
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
// configName is the ConfigMap the validator reads: the one this run created,
// or clusterValidatorNoConfigName.
func buildClusterValidatorJob(
	name, image, pullSecret, role, runID, configName string, noCleanup bool, env map[string]string,
	tolerations ...corev1.Toleration,
) *batchv1.Job {
	backoff := int32(0)
	grace := int64(clusterValidatorTerminationGrace / time.Second)
	// Pod shape mirrors the chart's nvcaop.clusterValidatorJobSpec, which
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
		ServiceAccountName:            clusterValidatorRBACName(role, runID),
		RestartPolicy:                 corev1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &grace,
		Tolerations:                   append(clusterValidatorTolerations(), tolerations...),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:      &runAsUser,
			RunAsGroup:     &runAsUser,
			FSGroup:        &runAsUser,
			RunAsNonRoot:   &runAsNonRoot,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
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
			},
			Env: []corev1.EnvVar{
				{Name: "VALIDATOR_CONFIG_NAMESPACE", Value: clusterValidatorNamespace},
				// Always set. An empty value is NOT "no config": the validator
				// falls back to its own default name, a ConfigMap anyone could
				// create, and would run whatever it holds.
				{Name: "VALIDATOR_CONFIG_NAME", Value: configName},
				{Name: "VALIDATOR_PREFLIGHT", Value: "true"},
				{Name: "VALIDATOR_ROLE", Value: role},
				{Name: validatorTimeoutEnv, Value: validatorRunTimeout(clusterValidatorTimeout)},
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
		// ConfigMap. The ServiceAccount, roles and bindings are left to the
		// deferred sweeps, or to a later run's orphan sweep. Sized above the
		// runner's own timeout so it never truncates a wait that is still
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
