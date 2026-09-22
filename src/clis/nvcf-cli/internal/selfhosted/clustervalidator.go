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
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"regexp"
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
	"k8s.io/client-go/kubernetes"
)

const (
	clusterValidatorNamespace    = "default"
	clusterValidatorName         = "nvcf-preflight-validator"
	clusterValidatorContainer    = "validator"
	clusterValidatorAppLabel     = "nvcf-cluster-validator"
	clusterValidatorPollInterval = 2 * time.Second
	clusterValidatorTTLSeconds   = int32(600)
	clusterValidatorHintURL      = "https://docs.nvidia.com/nvcf/self-managed-clusters#cluster-validator"
	// orphanValidatorRBACTTL is the minimum age before leftover validator RBAC
	// is reclaimed. Must exceed the validator timeout so a live run is never hit.
	orphanValidatorRBACTTL = 30 * time.Minute
)

// Vars (not consts) so tests can shorten without the full production budget.
var (
	clusterValidatorTimeout         = 5 * time.Minute
	clusterValidatorLogFetchTimeout = 10 * time.Second
)

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
	// Registries is a list of additional "host:port" registry endpoints to
	// probe for reachability in the control-plane validator ConfigMap (in
	// addition to nvcr.io which is always included). Ignored for the
	// compute-plane role.
	Registries []string
}

// Err is non-nil only when the run failed to execute (RBAC bootstrap,
// image pull, context timeout). A Passed=false verdict from the validator
// itself leaves Err nil.
type ClusterValidatorResult struct {
	Passed   bool
	ExitCode int32
	Logs     string
	JobName  string
	Err      error
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
		return runClusterValidator(ctx, client, p.Image, p.PullSecret, p.NoCleanup, p.Role, p.Registries)
	}
}

// Testable core. Pass a fake clientset to unit-test without a real cluster.
func runClusterValidator(ctx context.Context, client kubernetes.Interface, image, pullSecret string, noCleanup bool, role string, registries []string) ClusterValidatorResult {
	if image == "" {
		// Defensive: callers gate on configured image before invoking the
		// validator, so this branch shouldn't fire in normal use.
		return ClusterValidatorResult{Err: fmt.Errorf("cluster-validator image is empty")}
	}

	vctx, cancel := context.WithTimeout(ctx, clusterValidatorTimeout)
	defer cancel()

	// podMayBeRunning is true only between a successful Job create and a clean
	// wait. Both cleanup defers below key off it: outside that window nothing
	// is using the pull secret or the RBAC, so reclaiming them is safe, and
	// inside it reclaiming them breaks the pod that is still running.
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

	// Reclaim leftovers from dead runs before resolving this run's pull secret,
	// not after: the sweep matches on the shared name prefix, so running it
	// later can delete the Secret this run just selected.
	sweepOrphanClusterValidatorRBAC(vctx, client, orphanValidatorRBACTTL)

	// Resolver errors are non-fatal: fall through to the caller's value and
	// let waitForClusterValidatorJob surface ImagePullBackOff if needed.
	if resolved, err := resolveValidatorPullSecret(ctx, client, pullSecret, image, role, runID, noCleanup); err == nil {
		pullSecret = resolved
	}
	// Sweep only this role's managed pull secret, and only once the pod can no
	// longer need it. Sweeping unconditionally deletes the Secret out from
	// under a pod that is still retrying its pull, which the kubelet then
	// reports as FailedToRetrieveImagePullSecret. Operator-supplied secrets via
	// the flag aren't labeled by us and are skipped.
	// --no-cleanup is honoured here as it is for the RBAC below: an operator
	// who keeps the Job for debugging and then re-runs the preserved pod would
	// otherwise hit FailedToRetrieveImagePullSecret, the exact symptom the
	// per-role secret scoping exists to prevent.
	defer func() {
		if !noCleanup && !podMayBeRunning {
			sweepManagedPullSecrets(context.Background(), client, role, runID)
		}
	}()

	// Register the cleanup before the bootstrap, not after. ensureClusterValidatorRBAC
	// creates three objects in sequence and returns on the first failure, so a
	// kubeconfig that can create a ServiceAccount but not a cluster-scoped
	// ClusterRole would otherwise abandon the ServiceAccount on every attempt.
	// The names are per-run now, so nothing self-heals by reuse: under --wait
	// that leaks one object per poll. The sweep is name-scoped and
	// label-guarded, so running it when nothing was created is a no-op.
	defer func() {
		if !noCleanup && !podMayBeRunning {
			sweepClusterValidatorRBAC(context.Background(), client, role, runID)
		}
	}()
	if err := ensureClusterValidatorRBAC(vctx, client, role, runID, noCleanup); err != nil {
		return ClusterValidatorResult{Err: fmt.Errorf("bootstrapping validator RBAC: %w", err)}
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
		defer func() {
			if !noCleanup && !podMayBeRunning {
				sweepClusterValidatorConfig(context.Background(), client, runID)
			}
		}()
		if err := ensureClusterValidatorConfig(vctx, client, registries, runID, noCleanup); err != nil {
			// Non-fatal: continue without the ConfigMap; the validator skips
			// configurable reachability and enforcement checks silently unless
			// we surface this note in the transcript.
			configNote = fmt.Sprintf("note: validator config not applied (%v); reachability checks may be skipped", err)
		}
	}

	// Skipped under --no-cleanup: this runs before the new Job is created, so
	// otherwise the next same-role run destroys the Job the operator asked to
	// keep. The singleton guarantee is worth less than the artifact they kept.
	if !noCleanup {
		sweepPriorClusterValidatorJobs(vctx, client, role)
	}

	jobName := fmt.Sprintf("%s-%d", clusterValidatorName, time.Now().UnixNano())
	if _, err := client.BatchV1().Jobs(clusterValidatorNamespace).Create(
		vctx, buildClusterValidatorJob(jobName, image, pullSecret, role, runID, noCleanup), metav1.CreateOptions{},
	); err != nil {
		return ClusterValidatorResult{Err: fmt.Errorf("creating validator Job: %w", err)}
	}
	podMayBeRunning = true

	final, waitErr := waitForClusterValidatorJob(vctx, client, jobName)
	// A clean wait means the pod reached a terminal state, so the RBAC can go.
	// A timeout or pull failure leaves it running: pulling the RBAC then fills
	// the surviving transcript with "forbidden" and masks the real cause.
	podMayBeRunning = waitErr != nil

	// Fetch logs under a fresh ctx from the parent: vctx is expired on the
	// timeout path, and dropping the partial transcript hurts most there.
	logCtx, logCancel := context.WithTimeout(ctx, clusterValidatorLogFetchTimeout)
	defer logCancel()
	rawLogs, _ := fetchClusterValidatorLogs(logCtx, client, jobName)
	cleaned := cleanValidatorOutput(rawLogs)
	if configNote != "" {
		cleaned = configNote + "\n" + cleaned
	}

	if waitErr != nil {
		return ClusterValidatorResult{
			JobName: jobName,
			Logs:    cleaned,
			Err:     waitErr,
		}
	}

	// Separate short ctx so a slow log fetch can't burn the exit-code lookup's
	// budget and silently return -1 on clean completions.
	exitCtx, exitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer exitCancel()
	return ClusterValidatorResult{
		Passed:   final.Status.Succeeded > 0,
		ExitCode: containerExitCode(exitCtx, client, jobName),
		Logs:     cleaned,
		JobName:  jobName,
	}
}

// Creates the SA/ClusterRole/ClusterRoleBinding the validator pod runs under,
// idempotent via AlreadyExists tolerance. ClusterRole uses update-or-create so
// newer CLI versions replace stale rules without the operator needing to delete.
func ensureClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface, role, runID string, preserve bool) error {
	roleLabels := clusterValidatorRoleLabelsPreserved(role, preserve)
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
		return fmt.Errorf("create service account: %w", err)
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: roleLabels},
		Rules: []rbacv1.PolicyRule{
			// Read-only: cluster inventory and configuration.
			{APIGroups: []string{""}, Resources: []string{"nodes", "configmaps"}, Verbs: []string{"get", "list", "watch"}},
			// Read + write: enforcement checks create and delete probe namespaces
			// and pods; the active-LB check creates and deletes a probe service.
			{APIGroups: []string{""}, Resources: []string{"namespaces", "pods", "services"}, Verbs: []string{"get", "list", "watch", "create", "delete"}},
			// Pod log subresource: read probe output without exec.
			{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"csidrivers", "storageclasses"}, Verbs: []string{"get", "list"}},
			// NetworkPolicies: read for CNI detection; write for enforcement
			// check which creates/updates/deletes policies in the temp namespace.
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"get", "list", "create", "update", "delete"}},
			{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}, Verbs: []string{"get", "list"}},
			// Deployments/StatefulSets: list for Tier-1/Tier-2 HA readiness checks.
			// DaemonSets: create/delete for the node-to-node DaemonSet probe; list to watch pod readiness.
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{"apps"}, Resources: []string{"daemonsets"}, Verbs: []string{"get", "list", "create", "delete"}},
			// Gateway API: control-plane gateway and route health checks.
			{APIGroups: []string{"gateway.networking.k8s.io"}, Resources: []string{"gatewayclasses", "gateways", "httproutes", "grpcroutes"}, Verbs: []string{"get", "list"}},
			{NonResourceURLs: []string{"/readyz", "/version", "/healthz"}, Verbs: []string{"get"}},
		},
	}
	// Create only, for the same reason as the ServiceAccount above. The name is
	// unique per run, so there are no stale rules from an older CLI to refresh
	// and nothing legitimate to overwrite.
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create cluster role: %w", err)
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
		return fmt.Errorf("create cluster role binding: %w", err)
	}
	return nil
}

func clusterValidatorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       clusterValidatorAppLabel,
		"app.kubernetes.io/managed-by": "nvcf-cli",
		"app.kubernetes.io/component":  "preflight",
	}
}

// clusterValidatorRoleLabels adds the role so the prior-run sweep only matches
// Jobs from the same role. Without it, a ModeSingle run deletes the other
// role's Job mid-command, including under --no-cleanup, which makes the
// printed `kubectl logs job/...` hint 404.
func clusterValidatorRoleLabels(role string) map[string]string {
	return clusterValidatorRoleLabelsPreserved(role, false)
}

// clusterValidatorRoleLabelsPreserved adds the preserve marker when the run was
// asked to keep its artifacts, so the orphan sweeper leaves them alone.
func clusterValidatorRoleLabelsPreserved(role string, preserve bool) map[string]string {
	l := clusterValidatorLabels()
	l[clusterValidatorRoleLabel] = role
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
func clusterValidatorConfigLabels(preserve bool) map[string]string {
	l := clusterValidatorLabels()
	if preserve {
		l[clusterValidatorPreserveLabel] = "true"
	}
	return l
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
	cutoff := metav1.Time{Time: time.Now().Add(-ttl)}

	// reclaimable requires the generated name as well as the labels and the
	// age. Labels alone are three public constants and can be copied onto
	// anything, and this deletes cluster-scoped objects with errors swallowed.
	// Network-check ConfigMaps carry their own generated-name prefix.
	reclaimableConfig := func(o metav1.Object) bool {
		if !strings.HasPrefix(o.GetName(), clusterValidatorConfigName) {
			return false
		}
		if o.GetLabels()[clusterValidatorPreserveLabel] == "true" {
			return false
		}
		ts := o.GetCreationTimestamp()
		return ts.Before(&cutoff)
	}

	// Pull secrets carry the other generated-name prefix.
	reclaimableSecret := func(o metav1.Object) bool {
		if !strings.HasPrefix(o.GetName(), validatorPullSecretName) {
			return false
		}
		if o.GetLabels()[clusterValidatorPreserveLabel] == "true" {
			return false
		}
		ts := o.GetCreationTimestamp()
		return ts.Before(&cutoff)
	}

	reclaimable := func(o metav1.Object) bool {
		if !strings.HasPrefix(o.GetName(), clusterValidatorName) {
			return false
		}
		if o.GetLabels()[clusterValidatorPreserveLabel] == "true" {
			return false
		}
		ts := o.GetCreationTimestamp()
		return ts.Before(&cutoff)
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
}

// sweepClusterValidatorRBAC removes the SA, ClusterRole, and ClusterRoleBinding
// created by ensureClusterValidatorRBAC. Called after Job completion (when
// --no-cleanup is not set) to close the window where the elevated ClusterRole
// exists. Each run mints its own names, so nothing is reused and anything left
// behind is reclaimed by sweepOrphanClusterValidatorRBAC instead.
// Errors are swallowed: stale RBAC is preferable to failing the result.
func sweepClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface, role, runID string) {
	name := clusterValidatorRBACName(role, runID)

	// Delete by name, but only what we own. These are cluster-scoped objects
	// and the errors here are swallowed, so an operator-owned ClusterRole with
	// a colliding name would otherwise vanish with no diagnostic at all.
	if crb, err := client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{}); err == nil &&
		hasValidatorManagedLabels(crb.Labels) {
		_ = client.RbacV1().ClusterRoleBindings().Delete(ctx, name, deleteExactly(crb))
	}
	if cr, err := client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{}); err == nil &&
		hasValidatorManagedLabels(cr.Labels) {
		_ = client.RbacV1().ClusterRoles().Delete(ctx, name, deleteExactly(cr))
	}
	sa := client.CoreV1().ServiceAccounts(clusterValidatorNamespace)
	if acct, err := sa.Get(ctx, name, metav1.GetOptions{}); err == nil &&
		hasValidatorManagedLabels(acct.Labels) {
		_ = sa.Delete(ctx, name, deleteExactly(acct))
	}
}

// Errors are swallowed: a stale Job is preferable to blocking the new run.
func sweepPriorClusterValidatorJobs(ctx context.Context, client kubernetes.Interface, role string) {
	// Scope to this role: in ModeSingle both roles run against the same
	// cluster, so an unscoped selector deletes the other role's Job mid-command.
	//
	// List-then-delete rather than DeleteCollection: the name has to be checked
	// too, which a collection selector cannot express, and the labels alone are
	// public constants that anything could carry.
	jobs := client.BatchV1().Jobs(clusterValidatorNamespace)
	l, err := jobs.List(ctx, metav1.ListOptions{LabelSelector: validatorRoleSelector(role)})
	if err != nil {
		return
	}
	propagation := metav1.DeletePropagationBackground
	for i := range l.Items {
		if !strings.HasPrefix(l.Items[i].Name, clusterValidatorName) {
			continue
		}
		// A Job an operator kept with --no-cleanup survives later ordinary
		// runs too. The current run already skips this sweep under that flag,
		// but without the marker the next run of the same role deletes the Job
		// while its ConfigMap, RBAC and pull secret survive, leaving the
		// preserved artifacts pointing at a Job that no longer exists.
		if l.Items[i].Labels[clusterValidatorPreserveLabel] == "true" {
			continue
		}
		opts := deleteExactly(&l.Items[i])
		opts.PropagationPolicy = &propagation
		_ = jobs.Delete(ctx, l.Items[i].Name, opts)
	}
}

// sweepManagedPullSecrets removes any docker-registry secrets in
// clusterValidatorNamespace that we previously created (mirrored from
// another namespace or minted from NGC_API_KEY). Called after the Job
// terminates so NGC credentials don't persist across preflight runs.
//
// Operator-supplied secrets via --cluster-validator-pull-secret aren't
// labeled by us and are skipped by the selector. Errors are swallowed:
// failing to clean up is preferable to failing the check itself.
func sweepManagedPullSecrets(ctx context.Context, client kubernetes.Interface, role, runID string) {
	// Same reasoning as sweepPriorClusterValidatorJobs: the generated name is
	// part of the ownership test, not just the labels.
	secrets := client.CoreV1().Secrets(clusterValidatorNamespace)
	l, err := secrets.List(ctx, metav1.ListOptions{LabelSelector: validatorRoleSelector(role)})
	if err != nil {
		return
	}
	for i := range l.Items {
		// Only this run's Secret. The name is unguessable and unique per run,
		// so a sweep can no longer take out a Secret another run selected.
		if l.Items[i].Name != validatorPullSecretRunName(role, runID) {
			continue
		}
		_ = secrets.Delete(ctx, l.Items[i].Name, deleteExactly(&l.Items[i]))
	}
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
// --no-cleanup. The orphan sweeper skips them: without it a preserved run is
// reclaimed anyway once it is older than the TTL, which makes the flag mean
// "keep for 30 minutes".
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

// controlPlaneValidatorConfigTemplate is the baseline network-check ConfigMap for
// control-plane preflight: nvcr.io reachability (critical) and NetworkPolicy
// enforcement (non-critical). Extra registries are appended as non-critical probes.
const controlPlaneValidatorConfigTemplate = `reachability:
  endpoints:
    - name: nvcr.io
      host: nvcr.io
      port: 443
      protocol: tcp+tls
      critical: true
enforcement:
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

// ensureClusterValidatorConfig creates or updates the network-check ConfigMap.
// extraRegistries are added as non-critical tcp+tls probes. Best-effort: the
// validator skips configurable checks when the ConfigMap is absent.
func ensureClusterValidatorConfig(ctx context.Context, client kubernetes.Interface, extraRegistries []string, runID string, preserve bool) error {
	content := buildControlPlaneValidatorConfig(extraRegistries)
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorConfigRunName(runID),
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorConfigLabels(preserve),
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

	// Always update so a newer CLI version's config (or new registries) replaces
	// stale content, but never overwrite a ConfigMap we do not own.
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

// buildControlPlaneValidatorConfig assembles the network-check ConfigMap YAML
// from the baseline template plus any operator-supplied extra registries.
func buildControlPlaneValidatorConfig(extraRegistries []string) string {
	if len(extraRegistries) == 0 {
		return controlPlaneValidatorConfigTemplate
	}

	// Parse host:port entries and append as non-critical tcp+tls endpoints.
	var extra strings.Builder
	for _, reg := range extraRegistries {
		host, port := parseRegistryHostPort(reg)
		if host == "" {
			continue
		}
		// Append under the existing reachability.endpoints list. Quote the host:
		// it comes from operator input, and parseRegistryHostPort passes a value
		// it cannot split through verbatim. An unquoted "[" or embedded newline
		// would make the whole ConfigMap unparseable, and the validator then
		// silently drops every reachability and enforcement check.
		fmt.Fprintf(&extra,
			"    - name: %q\n      host: %q\n      port: %d\n      protocol: tcp+tls\n      critical: false\n",
			host, host, port)
	}
	if extra.Len() == 0 {
		return controlPlaneValidatorConfigTemplate
	}

	// Insert extra endpoints after the nvcr.io entry (before the enforcement block).
	return strings.Replace(controlPlaneValidatorConfigTemplate,
		"enforcement:", extra.String()+"enforcement:", 1)
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
		// No port present (e.g. "nvcr.io") - return the input as-is.
		return s, 443
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
func buildClusterValidatorJob(name, image, pullSecret, role, runID string, noCleanup bool) *batchv1.Job {
	backoff := int32(0)
	activeDeadline := int64(clusterValidatorTimeout/time.Second) + 60
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
		Tolerations:        clusterValidatorTolerations(),
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
	if pullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: pullSecret}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorRoleLabelsPreserved(role, noCleanup),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			// Without a deadline an ImagePullBackOff Job never reaches a
			// terminal state, so TTLSecondsAfterFinished never fires and the
			// Job, its RBAC and its pull secret persist indefinitely. The
			// deferred sweeps are suppressed on that path by design, because
			// the pod may still be running, so this is the only reclaim.
			// Sized above the runner's own budget so it never truncates a wait
			// that is still making progress.
			ActiveDeadlineSeconds: &activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: clusterValidatorRoleLabels(role)},
				Spec:       podSpec,
			},
		},
	}
	if !noCleanup {
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
	for {
		job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			// If the deadline fired between ticker and Get, surface the wait
			// wording the select branch would have used.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("waiting for job %s: %w", jobName, ctxErr)
			}
			return nil, fmt.Errorf("get job %s: %w", jobName, err)
		}
		if job.Status.Succeeded > 0 || job.Status.Failed > 0 {
			return job, nil
		}
		if reason, msg := podPullFailureReason(ctx, client, jobName); reason != "" {
			return job, fmt.Errorf("validator pod cannot pull image (%s): %s", reason, msg)
		}
		select {
		case <-ctx.Done():
			return job, fmt.Errorf("waiting for job %s: %w", jobName, ctx.Err())
		case <-ticker.C:
		}
	}
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

// Informational only; the canonical pass/fail signal is Job.Status.Succeeded.
// Returns -1 when the exit code can't be read.
func containerExitCode(ctx context.Context, client kubernetes.Interface, jobName string) int32 {
	podName, _ := podNameForClusterValidatorJob(ctx, client, jobName)
	if podName == "" {
		return -1
	}
	pod, err := client.CoreV1().Pods(clusterValidatorNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return -1
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == clusterValidatorContainer && cs.State.Terminated != nil {
			return cs.State.Terminated.ExitCode
		}
	}
	return -1
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
// Errors are swallowed, as with the other sweeps.
func sweepClusterValidatorConfig(ctx context.Context, client kubernetes.Interface, runID string) {
	cms := client.CoreV1().ConfigMaps(clusterValidatorNamespace)
	name := clusterValidatorConfigRunName(runID)
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if err != nil || !hasValidatorManagedLabels(cm.Labels) {
		return
	}
	_ = cms.Delete(ctx, name, deleteExactly(cm))
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
