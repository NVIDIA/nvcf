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

package clusteragent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"nvcf-cli/internal/logging"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// Maintenance constants. These mirror the NVCA operator contract defined in
// nvca/pkg/operator/reconcile/backendk8scache.go. Drain adds the
// CordonAndDrainMaintenance flag to the NVCFBackend CR's
// spec.overrides.featureGate.values; the operator's own reconcile loop
// regenerates agent-config from the CR and rolls out NVCA itself. The CLI
// never writes agent-config or the NVCA Deployment directly: the operator
// treats both as generated artifacts and reverts direct edits on its next
// reconcile (informer resync, CR change, or operator restart).
const (
	agentConfigConfigMapName      = "agent-config"
	agentConfigKey                = "config.yaml"
	nvcaDeploymentName            = "nvca"
	cordonAndDrainFeatureFlag     = "CordonAndDrainMaintenance"
	cordonMaintenanceFeatureFlag  = "CordonMaintenance"
	maintenanceModeCordonAndDrain = "CordonAndDrain"

	// Namespace defaults applied when the NVCFBackend CR leaves them empty,
	// matching DefaultNVCASystemNamespace / DefaultNVCARequestsNamespace upstream.
	defaultSystemNamespace   = "nvca-system"
	defaultRequestsNamespace = "nvcf-backend"
)

// rolloutPollInterval bounds how often waitForRollout polls the Deployment. It
// is a var so tests can shorten it.
var rolloutPollInterval = 2 * time.Second

// killDeletionPollInterval bounds how often deleteICMSRequest polls for the
// ICMSRequest to actually disappear after Delete is called. It is a var so
// tests can shorten it.
var killDeletionPollInterval = 2 * time.Second

// k8sMaintainer mutates NVCA state on a compute-plane cluster. It uses the
// dynamic client for the ICMSRequest and NVCFBackend custom resources and the
// typed clientset for the agent-config ConfigMap and the NVCA Deployment.
type k8sMaintainer struct {
	dc dynamic.Interface
	cs kubernetes.Interface
}

// NewK8sMaintainer returns an AgentMaintainer backed by the Kubernetes dynamic
// client (custom resources) and typed clientset (ConfigMap/Deployment).
func NewK8sMaintainer(dc dynamic.Interface, cs kubernetes.Interface) AgentMaintainer {
	return &k8sMaintainer{dc: dc, cs: cs}
}

// ResolveCluster reads the NVCFBackend CR and returns the cluster identity and
// namespace layout, applying defaults for unset namespaces.
func (m *k8sMaintainer) ResolveCluster(ctx context.Context, backendNS string) (*ClusterTarget, error) {
	item, err := m.getNVCFBackendObject(ctx, backendNS)
	if err != nil {
		return nil, err
	}

	obj := item.Object
	return &ClusterTarget{
		ClusterID:         firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "clusterId"), nestedString(obj, "spec", "clusterConfig", "clusterID")),
		ClusterName:       nestedString(obj, "spec", "clusterConfig", "clusterName"),
		SystemNamespace:   firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "systemNamespace"), defaultSystemNamespace),
		RequestsNamespace: firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "requestsNamespace"), defaultRequestsNamespace),
	}, nil
}

// getNVCFBackendObject fetches the single NVCFBackend CR in backendNS. The
// NVCA operator contract guarantees exactly one per compute-plane cluster.
func (m *k8sMaintainer) getNVCFBackendObject(ctx context.Context, backendNS string) (*unstructured.Unstructured, error) {
	list, err := m.dc.Resource(nvcfBackendGVR).Namespace(backendNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, wrapCRDError(err, "NVCFBackend", backendNS)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no NVCFBackend resource found in namespace %q; is this context pointed at an NVCF compute-plane cluster (try --backend-namespace)?", backendNS)
	}
	return &list.Items[0], nil
}

// resolveAndVerify is the common preamble: resolve the cluster, then enforce the
// optional --expect-cluster-id guard.
func (m *k8sMaintainer) resolveAndVerify(ctx context.Context, backendNS, expectClusterID string) (*ClusterTarget, error) {
	target, err := m.ResolveCluster(ctx, backendNS)
	if err != nil {
		return nil, err
	}
	if err := verifyCluster(target, expectClusterID); err != nil {
		return nil, err
	}
	return target, nil
}

// verifyCluster refuses to proceed when --expect-cluster-id was supplied and does
// not match the connected cluster. An empty expectClusterID trusts the context.
func verifyCluster(target *ClusterTarget, expectClusterID string) error {
	if expectClusterID == "" {
		return nil
	}
	if expectClusterID == target.ClusterID || expectClusterID == target.ClusterName {
		return nil
	}
	return fmt.Errorf("refusing to proceed: --expect-cluster-id %q does not match the connected cluster %s; check --compute-plane-context", expectClusterID, clusterLabel(target))
}

func clusterLabel(target *ClusterTarget) string {
	switch {
	case target.ClusterName != "" && target.ClusterID != "":
		return fmt.Sprintf("%s (%s)", target.ClusterName, target.ClusterID)
	case target.ClusterName != "":
		return target.ClusterName
	case target.ClusterID != "":
		return target.ClusterID
	default:
		return "(unknown identity)"
	}
}

// Drain puts the cluster into CordonAndDrain maintenance.
func (m *k8sMaintainer) Drain(ctx context.Context, opts DrainOptions) (*DrainResult, error) {
	return m.setMaintenance(ctx, opts, true)
}

// Undrain returns the cluster to normal operation.
func (m *k8sMaintainer) Undrain(ctx context.Context, opts DrainOptions) (*DrainResult, error) {
	return m.setMaintenance(ctx, opts, false)
}

func (m *k8sMaintainer) setMaintenance(ctx context.Context, opts DrainOptions, drain bool) (*DrainResult, error) {
	target, err := m.resolveAndVerify(ctx, opts.BackendNS, opts.ExpectClusterID)
	if err != nil {
		return nil, err
	}
	systemNS := target.SystemNamespace

	result := &DrainResult{
		ClusterID:       target.ClusterID,
		ClusterName:     target.ClusterName,
		SystemNamespace: systemNS,
		DryRun:          opts.DryRun,
	}
	if drain {
		result.Mode = maintenanceModeCordonAndDrain
	}

	if err := m.checkMaintenanceConflict(ctx, opts.BackendNS, drain); err != nil {
		return nil, err
	}

	if opts.DryRun {
		has, err := m.nvcfBackendHasMaintenanceFlag(ctx, opts.BackendNS)
		if err != nil {
			return nil, err
		}
		result.ConfigChanged = has != drain
		if result.ConfigChanged {
			result.Message = "dry run: would update the NVCFBackend CR; the NVCA operator would then regenerate agent-config and roll out NVCA"
		} else {
			result.Message = "dry run: already in the requested state; no change"
		}
		return result, nil
	}

	changed, err := m.patchMaintenanceFeatureFlag(ctx, opts.BackendNS, drain)
	if err != nil {
		return nil, err
	}
	result.ConfigChanged = changed
	if !changed {
		// Idempotent: nothing to wait for. Re-running the same command is
		// always safe here (unlike the old ConfigMap/Deployment-restart
		// approach), since the CLI no longer performs a mutation the
		// operator could race with; it only submits a desired-state change
		// the operator's own reconcile owns.
		//
		// Behavior change from the old agent-config/Deployment-restart
		// approach: --force no longer has an effect here. It used to also
		// retrigger the NVCA restart even when the desired state was
		// already reached, which let a stuck rollout be kicked by
		// re-running with --force. Since the CLI no longer performs that
		// restart directly, there is nothing left for --force to retrigger
		// when the CR is already in the desired state; only the operator's
		// own reconcile can recover a stuck rollout now.
		result.Message = "already in the requested state; no change"
		return result, nil
	}
	result.RolloutTriggered = true

	switch {
	case opts.Force:
		result.Message = "NVCFBackend updated; not waiting for the NVCA operator's rollout (--force)"
	case opts.Timeout <= 0:
		result.Message = "NVCFBackend updated; not waiting for the NVCA operator's rollout (--timeout 0)"
	default:
		if err := m.waitForMaintenanceRollout(ctx, opts.BackendNS, systemNS, opts.Timeout, drain); err != nil {
			result.Message = fmt.Sprintf("NVCFBackend updated, but the NVCA operator has not finished reconciling and rolling out the change: %v", err)
			return result, nil
		}
		result.RolloutComplete = true
	}
	return result, nil
}

// nvcfBackendHasMaintenanceFlag reports whether the NVCFBackend CR's
// spec.overrides.featureGate.values in backendNS currently contains
// cordonAndDrainFeatureFlag.
func (m *k8sMaintainer) nvcfBackendHasMaintenanceFlag(ctx context.Context, backendNS string) (bool, error) {
	obj, err := m.getNVCFBackendObject(ctx, backendNS)
	if err != nil {
		return false, err
	}
	values, _, err := unstructured.NestedStringSlice(obj.Object, "spec", "overrides", "featureGate", "values")
	if err != nil {
		return false, fmt.Errorf("reading NVCFBackend spec.overrides.featureGate.values: %w", err)
	}
	return slices.Contains(values, cordonAndDrainFeatureFlag), nil
}

// checkMaintenanceConflict reads the NVCFBackend CR's base
// spec.featureGate.values and returns an error if the operator's
// mergeOverrides logic would keep the requested drain/undrain from ever
// taking effect, even though patching spec.overrides would succeed:
//
//   - drain: if the base spec already sets cordonMaintenanceFeatureFlag, the
//     operator prefers CordonMaintenance over CordonAndDrainMaintenance
//     whenever both are present in the merged result, so adding
//     cordonAndDrainFeatureFlag to the overrides would be silently dropped.
//   - undrain: if the base spec already sets cordonAndDrainFeatureFlag, the
//     operator's merge keeps a maintenance flag that was present in either
//     the base spec or the overrides, so removing it from overrides alone
//     would not clear it from the merged result.
func (m *k8sMaintainer) checkMaintenanceConflict(ctx context.Context, backendNS string, drain bool) error {
	obj, err := m.getNVCFBackendObject(ctx, backendNS)
	if err != nil {
		return err
	}
	baseValues, _, err := unstructured.NestedStringSlice(obj.Object, "spec", "featureGate", "values")
	if err != nil {
		return fmt.Errorf("reading NVCFBackend spec.featureGate.values: %w", err)
	}
	switch {
	case drain && slices.Contains(baseValues, cordonMaintenanceFeatureFlag):
		return fmt.Errorf("cannot drain: NVCFBackend spec.featureGate.values already sets %q, which the NVCA operator prefers over %q, so drain would have no effect; remove %q from the base spec first", cordonMaintenanceFeatureFlag, cordonAndDrainFeatureFlag, cordonMaintenanceFeatureFlag)
	case !drain && slices.Contains(baseValues, cordonAndDrainFeatureFlag):
		return fmt.Errorf("cannot undrain: NVCFBackend spec.featureGate.values already sets %q, which the NVCA operator keeps regardless of overrides, so undrain would have no effect; remove %q from the base spec first", cordonAndDrainFeatureFlag, cordonAndDrainFeatureFlag)
	}
	return nil
}

// patchMaintenanceFeatureFlag adds or removes cordonAndDrainFeatureFlag on
// the NVCFBackend CR's spec.overrides.featureGate.values, retrying on update
// conflicts. It reports whether the value actually changed.
//
// This patches the NVCFBackend CR rather than agent-config directly: the
// NVCA operator treats agent-config as a fully generated artifact rebuilt
// from this CR on every reconcile (informer resync, CR change, operator
// restart), so a direct ConfigMap edit gets silently reverted on the
// operator's next reconcile. Patching spec.overrides here (rather than the
// base spec.featureGate) lets the operator's own additive merge apply it and
// its reconcile regenerate agent-config correctly and roll out NVCA itself.
func (m *k8sMaintainer) patchMaintenanceFeatureFlag(ctx context.Context, backendNS string, drain bool) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := m.getNVCFBackendObject(ctx, backendNS)
		if err != nil {
			return err
		}
		values, _, err := unstructured.NestedStringSlice(obj.Object, "spec", "overrides", "featureGate", "values")
		if err != nil {
			return fmt.Errorf("reading NVCFBackend spec.overrides.featureGate.values: %w", err)
		}
		next := setMaintenanceFeatureFlag(values, drain)
		if slices.Equal(values, next) {
			changed = false
			return nil
		}
		if err := unstructured.SetNestedStringSlice(obj.Object, next, "spec", "overrides", "featureGate", "values"); err != nil {
			return fmt.Errorf("writing NVCFBackend spec.overrides.featureGate.values: %w", err)
		}
		if _, err := m.dc.Resource(nvcfBackendGVR).Namespace(backendNS).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// setMaintenanceFeatureFlag returns values with cordonAndDrainFeatureFlag
// added (drain) or removed (undrain), preserving the order and content of
// every other entry.
func setMaintenanceFeatureFlag(values []string, drain bool) []string {
	next := make([]string, 0, len(values)+1)
	has := false
	for _, v := range values {
		if v == cordonAndDrainFeatureFlag {
			has = true
			if !drain {
				continue
			}
		}
		next = append(next, v)
	}
	if drain && !has {
		next = append(next, cordonAndDrainFeatureFlag)
	}
	return next
}

// getAgentConfig fetches the agent-config ConfigMap and its config.yaml payload,
// translating common failures into actionable messages. It is read-only: the
// CLI never writes this ConfigMap (see patchMaintenanceFeatureFlag).
func (m *k8sMaintainer) getAgentConfig(ctx context.Context, systemNS string) (*corev1.ConfigMap, string, error) {
	cm, err := m.cs.CoreV1().ConfigMaps(systemNS).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return nil, "", fmt.Errorf("agent-config ConfigMap not found in namespace %s; is NVCA installed on this cluster?", systemNS)
		case apierrors.IsForbidden(err):
			return nil, "", fmt.Errorf("not permitted to read the agent-config ConfigMap in namespace %s: %w", systemNS, err)
		default:
			return nil, "", fmt.Errorf("failed to read agent-config ConfigMap in namespace %s: %w", systemNS, err)
		}
	}
	cur, ok := cm.Data[agentConfigKey]
	if !ok {
		return nil, "", fmt.Errorf("agent-config ConfigMap %s/%s is missing the %q key", systemNS, agentConfigConfigMapName, agentConfigKey)
	}
	return cm, cur, nil
}

// waitForMaintenanceRollout polls until the NVCA operator has both
// regenerated agent-config to reflect the new maintenance state and rolled
// the NVCA Deployment out to match, or timeout elapses.
//
// Both conditions are checked together deliberately: checking only the
// Deployment's rollout status is not sufficient, because immediately after
// patching the CR the Deployment may still trivially satisfy the "rollout
// complete" condition from before the operator has even started reconciling
// the change, which would report success without the operator having done
// anything yet.
func (m *k8sMaintainer) waitForMaintenanceRollout(ctx context.Context, backendNS, systemNS string, timeout time.Duration, drain bool) error {
	deadline := time.Now().Add(timeout)
	for {
		configReady := false
		if _, cur, err := m.getAgentConfig(ctx, systemNS); err == nil {
			configReady = configHasFeatureFlag(cur, cordonAndDrainFeatureFlag) == drain
		}

		rolloutReady := false
		deploy, err := m.cs.AppsV1().Deployments(systemNS).Get(ctx, nvcaDeploymentName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			rolloutReady = false
		case err == nil:
			desired := int32(1)
			if deploy.Spec.Replicas != nil {
				desired = *deploy.Spec.Replicas
			}
			rolloutReady = deploy.Status.ObservedGeneration >= deploy.Generation &&
				deploy.Status.UpdatedReplicas == desired &&
				deploy.Status.AvailableReplicas == desired &&
				deploy.Status.UnavailableReplicas == 0
		}

		if configReady && rolloutReady {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for the NVCA operator to reconcile NVCFBackend %s and roll out %s/%s", backendNS, systemNS, nvcaDeploymentName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rolloutPollInterval):
		}
	}
}

// KillFunction terminates every ICMSRequest matching functionID (and versionID
// when set). Zero matches is an error.
func (m *k8sMaintainer) KillFunction(ctx context.Context, functionID, versionID string, opts KillOptions) (*KillResult, error) {
	target, err := m.resolveAndVerify(ctx, opts.BackendNS, opts.ExpectClusterID)
	if err != nil {
		return nil, err
	}
	if err := validateKillTimeout(opts.Timeout); err != nil {
		return nil, err
	}

	result, failures, err := m.killMatching(ctx, target, opts, func(fid, vid string) bool {
		return fid == functionID && (versionID == "" || vid == versionID)
	})
	if err != nil {
		return nil, err
	}
	if len(result.Affected) == 0 {
		if versionID != "" {
			return nil, fmt.Errorf("no scheduled function found for function %s version %s in namespace %s", functionID, versionID, target.RequestsNamespace)
		}
		return nil, fmt.Errorf("no scheduled function found for function %s in namespace %s", functionID, target.RequestsNamespace)
	}
	return result, aggregateKillError(result, failures)
}

// KillAll terminates every ICMSRequest on the cluster. An empty cluster returns
// an empty result and no error.
func (m *k8sMaintainer) KillAll(ctx context.Context, opts KillOptions) (*KillResult, error) {
	target, err := m.resolveAndVerify(ctx, opts.BackendNS, opts.ExpectClusterID)
	if err != nil {
		return nil, err
	}
	if err := validateKillTimeout(opts.Timeout); err != nil {
		return nil, err
	}

	result, failures, err := m.killMatching(ctx, target, opts, func(string, string) bool { return true })
	if err != nil {
		return nil, err
	}
	return result, aggregateKillError(result, failures)
}

// validateKillTimeout rejects a negative --timeout. Zero is valid: it means
// "use DefaultKillTimeout" (handled in killMatching).
func validateKillTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return fmt.Errorf("--timeout must not be negative, got %s", timeout)
	}
	return nil
}

// killMatching lists ICMSRequests in the requests namespace, selects the ones
// the predicate accepts (in deterministic order), and deletes each unless DryRun.
// Scope is intentionally limited to target.RequestsNamespace: the NVCA operator
// contract guarantees all ICMSRequests for a cluster live in the single namespace
// recorded in the NVCFBackend CR's requestsNamespace field. The inspector's
// all-namespace scan is a visibility-only read path that tolerates stale state;
// kill operations use the authoritative namespace to avoid accidental cross-cluster deletions.
// The second return value collects the underlying error for each per-item
// delete failure (distinct from the KilledRequest.Error strings, which exist
// for JSON/text output). Callers wrap these into the aggregate error so
// errors.Is/errors.As can still reach the original cause.
func (m *k8sMaintainer) killMatching(ctx context.Context, target *ClusterTarget, opts KillOptions, match func(functionID, versionID string) bool) (*KillResult, []error, error) {
	items, err := listICMSRequests(ctx, m.dc, target.RequestsNamespace)
	if err != nil {
		return nil, nil, err
	}
	sortICMSRequests(items)

	result := &KillResult{
		ClusterID:         target.ClusterID,
		ClusterName:       target.ClusterName,
		RequestsNamespace: target.RequestsNamespace,
		Reason:            opts.Reason,
		DryRun:            opts.DryRun,
		Affected:          []KilledRequest{},
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultKillTimeout
	}

	var failures []error
	for i := range items {
		fid, vid := functionIdentity(items[i].Object)
		if !match(fid, vid) {
			continue
		}
		killed := KilledRequest{
			Namespace:         items[i].GetNamespace(),
			Name:              items[i].GetName(),
			FunctionID:        fid,
			FunctionVersionID: vid,
		}
		if !opts.DryRun {
			// Deleting the ICMSRequest CR alone never evicts the workload
			// (see evictInstances doc comment), so drive the real eviction
			// ourselves first. A failure here must stop this item rather
			// than fall through to delete: with --force in particular,
			// proceeding would strip the finalizer and report success while
			// the workload (pod or MiniService) may still be running.
			if err := m.evictInstances(ctx, killed.Namespace, &items[i]); err != nil {
				killed.Error = fmt.Sprintf("evicting instances: %v", err)
				result.FailedCount++
				failures = append(failures, fmt.Errorf("%s/%s (function=%s version=%s cluster=%s): evicting instances: %w",
					killed.Namespace, killed.Name, killed.FunctionID, killed.FunctionVersionID, clusterLabel(target), err))
				result.Affected = append(result.Affected, killed)
				continue
			}
			terminating, err := m.deleteICMSRequest(ctx, killed.Namespace, killed.Name, opts.Force, timeout)
			switch {
			case err != nil:
				killed.Error = err.Error()
				result.FailedCount++
				failures = append(failures, fmt.Errorf("%s/%s: %w", killed.Namespace, killed.Name, err))
			case terminating:
				// The delete was accepted (deletionTimestamp set) but NVCA had not
				// removed its finalizer and evicted the workload by the deadline.
				// This is not a failure to report deletion as complete when it is not.
				killed.Terminating = true
				result.TerminatingCount++
			default:
				// Audit line for the termination, including the operator-supplied
				// reason. Carried in the result too, but this emits it to logs.
				logging.Info("terminated ICMSRequest %s/%s (function=%s version=%s) reason=%q",
					killed.Namespace, killed.Name, killed.FunctionID, killed.FunctionVersionID, opts.Reason)
			}
		}
		result.Affected = append(result.Affected, killed)
	}
	return result, failures, nil
}

// evictInstances directly terminates the instances an ICMSRequest tracks in
// status.instances (Pod or MiniService), and marks each one
// lastReportedStatus: "terminated" on that same CR.
//
// Deleting the ICMSRequest CR alone never evicts the workload: the NVCA
// reconciler's deletion-handling branch is a passive gate that only removes
// the finalizer once AllInstancesTerminatedAndReported is true for that CR's
// own status.instances (the pod is gone from Kubernetes AND
// lastReportedStatus == "terminated"). Nothing else in NVCA drives eviction
// or sets that field for a CLI-initiated kill: the only other code path that
// sets it is ApplyTerminationMessage, reachable only via a genuine upstream
// ICMS termination queue message, and it writes to the termination message's
// own CR, never back onto this one. So without this, the CR (and pod) can
// stay stuck behind the finalizer forever, regardless of --timeout.
//
// Performing both steps here satisfies the reconciler's own precondition, so
// its existing, unmodified logic clears the finalizer itself on its next
// pass.
//
// MiniService (Helm function) instances differ from Pod instances: deleting
// the MiniService object is itself sufficient to drive real teardown (its
// own controller, internal/miniservice/reconcile.go, actively deletes the
// rendered chart's objects, namespace, and cache entries on deletion, unlike
// ICMSRequest's passive gate), so no separate resource-deletion step is
// needed beyond the Delete call. The lastReportedStatus patch below is still
// required for both instance types: AllInstancesTerminatedAndReported checks
// it after the pod/MiniService-existence check regardless of type.
func (m *k8sMaintainer) evictInstances(ctx context.Context, namespace string, obj *unstructured.Unstructured) error {
	instances, found, err := unstructured.NestedMap(obj.Object, "status", "instances")
	if err != nil {
		return fmt.Errorf("reading status.instances: %w", err)
	}
	if !found || len(instances) == 0 {
		return nil
	}

	var errs []error
	terminated := map[string]string{}
	for id, raw := range instances {
		inst, ok := raw.(map[string]interface{})
		if !ok {
			errs = append(errs, fmt.Errorf("instance %s: status.instances record is not an object", id))
			continue
		}
		// instanceType is the current field; type is a legacy alias some
		// older records still carry (see extractInstances in
		// k8s_inspector.go, which reads both for the same reason).
		instanceType, _, _ := unstructured.NestedString(inst, "instanceType")
		if instanceType == "" {
			instanceType, _, _ = unstructured.NestedString(inst, "type")
		}
		var delErr error
		switch instanceType {
		case "", "Pod":
			delErr = m.cs.CoreV1().Pods(namespace).Delete(ctx, id, metav1.DeleteOptions{})
		case "MiniService":
			// Cluster-scoped: no .Namespace(...).
			delErr = m.dc.Resource(miniServiceGVR).Delete(ctx, id, metav1.DeleteOptions{})
		default:
			// Unrecognized instance type: do not guess which resource kind
			// to delete. Reported as a failure (rather than silently
			// skipped) so the caller does not proceed to delete the CR
			// while this instance's workload was never touched.
			errs = append(errs, fmt.Errorf("instance %s: unsupported instance type %q", id, instanceType))
			continue
		}
		if delErr != nil && !apierrors.IsNotFound(delErr) {
			errs = append(errs, fmt.Errorf("deleting %s instance %s: %w", firstNonEmpty(instanceType, "Pod"), id, delErr))
			continue
		}
		terminated[id] = "terminated"
	}
	if len(terminated) == 0 {
		return errors.Join(errs...)
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := m.dc.Resource(icmsRequestGVR).Namespace(namespace).Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		existing, _, _ := unstructured.NestedMap(latest.Object, "status", "instances")
		if existing == nil {
			existing = map[string]interface{}{}
		}
		// Merge lastReportedStatus into whatever is currently on the
		// server, rather than overwriting the whole instance record with
		// the pre-eviction snapshot: NVCA may have concurrently updated
		// other instance fields (attributes, timestamps) since obj was read.
		for id, status := range terminated {
			cur, ok := existing[id].(map[string]interface{})
			if !ok {
				cur = map[string]interface{}{"id": id}
			}
			cur["lastReportedStatus"] = status
			existing[id] = cur
		}
		if err := unstructured.SetNestedMap(latest.Object, existing, "status", "instances"); err != nil {
			return err
		}
		latest.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   icmsRequestGVR.Group,
			Version: icmsRequestGVR.Version,
			Kind:    "ICMSRequest",
		})
		_, err = m.dc.Resource(icmsRequestGVR).Namespace(namespace).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("patching instance status: %w", err))
	}
	return errors.Join(errs...)
}

// deleteICMSRequest deletes one ICMSRequest and waits up to timeout for it to
// actually disappear. When force is set, it first strips finalizers so a CR
// stuck Terminating is removed even if NVCA is not running.
//
// Delete() only guarantees the deletion was accepted: when the object carries
// a finalizer (nvca.finalizers.nvidia.io), the API server sets
// deletionTimestamp and returns success while the object, and the pod it
// owns, keep running until the NVCA reconciler removes the finalizer. Callers
// must not treat a nil error from Delete alone as "the resource is gone."
//
// Returns (terminating=true, nil) when the delete was accepted but the object
// still existed when the wait deadline elapsed. A NotFound at any point
// (delete or poll) is treated as success (the reconciler raced us).
func (m *k8sMaintainer) deleteICMSRequest(ctx context.Context, namespace, name string, force bool, timeout time.Duration) (bool, error) {
	if force {
		if err := m.stripFinalizers(ctx, namespace, name); err != nil {
			return false, err
		}
	}
	err := m.dc.Resource(icmsRequestGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return m.waitForICMSRequestGone(ctx, namespace, name, timeout)
}

// waitForICMSRequestGone polls until the ICMSRequest is gone or timeout
// elapses. It returns (true, nil) rather than an error on timeout: the
// request was validly accepted for deletion, it just has not finished yet.
// Both the Get call and the poll sleep are bounded by the deadline, so a slow
// or blocked API call cannot make the wait overrun the configured timeout,
// and a timeout shorter than killDeletionPollInterval is still honored
// instead of sleeping through the whole poll interval regardless.
func (m *k8sMaintainer) waitForICMSRequestGone(ctx context.Context, namespace, name string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		getCtx, cancel := context.WithDeadline(ctx, deadline)
		_, err := m.dc.Resource(icmsRequestGVR).Namespace(namespace).Get(getCtx, name, metav1.GetOptions{})
		// Read getCtx.Err() before cancel(): cancel() makes every derived
		// context report Canceled regardless of why it actually ended, so
		// this is the only point where it still reflects the real cause.
		localDeadlineExceeded := getCtx.Err() == context.DeadlineExceeded
		cancel()
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if ctx.Err() != nil {
				// The caller's own context ended, not our synthetic deadline.
				return false, ctx.Err()
			}
			if localDeadlineExceeded && errors.Is(err, context.DeadlineExceeded) {
				// Our per-Get deadline (== the overall deadline) is what ended
				// the call: treat exactly like a timeout that elapsed between
				// polls. Both checks matter: getCtx.Err() alone would also
				// match an unrelated client/transport-level timeout that
				// races with our deadline; errors.Is(err, ...) alone would
				// also match a spurious deadline-shaped error the transport
				// returns well before our deadline actually elapses. Only
				// requiring both guards against silently discarding a real,
				// unrelated error (e.g. Forbidden) that happens to land in
				// the same instant our deadline fires.
				return true, nil
			}
			return false, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true, nil
		}
		wait := killDeletionPollInterval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// stripFinalizers clears the finalizers on an ICMSRequest, mirroring the
// operator's forced-teardown path. The GVK must be set before a dynamic Update.
func (m *k8sMaintainer) stripFinalizers(ctx context.Context, namespace, name string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := m.dc.Resource(icmsRequestGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if len(latest.GetFinalizers()) == 0 {
			return nil
		}
		latest.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   icmsRequestGVR.Group,
			Version: icmsRequestGVR.Version,
			Kind:    "ICMSRequest",
		})
		latest.SetFinalizers(nil)
		_, err = m.dc.Resource(icmsRequestGVR).Namespace(namespace).Update(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

// aggregateKillError summarizes a kill outcome. failures carries the
// underlying per-item errors (wrapped with %w by the caller), so a caller
// inspecting the returned error with errors.Is/errors.As can still reach the
// original cause behind the summary text.
func aggregateKillError(result *KillResult, failures []error) error {
	switch {
	case result.FailedCount > 0:
		return fmt.Errorf("failed to terminate %d of %d ICMSRequest(s): %w", result.FailedCount, len(result.Affected), errors.Join(failures...))
	case result.TerminatingCount > 0:
		return fmt.Errorf("%d of %d ICMSRequest(s) still terminating: NVCA has not finished evicting the workload; re-check with cluster agent get-function", result.TerminatingCount, len(result.Affected))
	default:
		return nil
	}
}

// --- agent-config YAML edits ---
//
// These mirror the line-based edits in nvca/pkg/operator/cleanup/cleanup.go so
// the CLI changes config.yaml exactly as the operator does, preserving the rest
// of the file. A structured re-marshal was rejected because it reorders keys and
// drops comments. Missing sections degrade to a no-op rather than corrupting the
// file.

// configHasFeatureFlag reports whether featureFlag is listed in the
// featureFlags: section of configYAML. Unlike checking
// addFeatureFlagToConfig's return value for a no-op, this is a pure
// membership check: addFeatureFlagToConfig also returns configYAML
// unchanged when there is no featureFlags: (or even agent:) section to
// insert into at all, which would misreport an absent flag as present.
func configHasFeatureFlag(configYAML, featureFlag string) bool {
	inFlags := false
	for _, line := range strings.Split(configYAML, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "featureFlags:" {
			inFlags = true
			continue
		}
		if !inFlags {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			if trimmed == "- "+featureFlag {
				return true
			}
			continue
		}
		if trimmed != "" {
			inFlags = false
		}
	}
	return false
}

func addFeatureFlagToConfig(configYAML, featureFlag string) string {
	lines := strings.Split(configYAML, "\n")

	// Locate the featureFlags: section; check for duplicates only within it.
	featureFlagsIdx := -1
	inFlags := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "featureFlags:" {
			featureFlagsIdx = i
			inFlags = true
			continue
		}
		if inFlags {
			if strings.HasPrefix(trimmed, "- ") {
				if trimmed == "- "+featureFlag {
					return configYAML // already present in featureFlags section
				}
			} else if trimmed != "" {
				inFlags = false
			}
		}
	}

	if featureFlagsIdx >= 0 {
		lines = insertAfter(lines, featureFlagsIdx, "  - "+featureFlag)
		return strings.Join(lines, "\n")
	}

	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") == "agent:" {
			lines = insertAfter(lines, i, "  - "+featureFlag)
			lines = insertAfter(lines, i, "  featureFlags:")
			return strings.Join(lines, "\n")
		}
	}

	return configYAML
}

func insertAfter(lines []string, index int, newLine string) []string {
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:index+1]...)
	result = append(result, newLine)
	result = append(result, lines[index+1:]...)
	return result
}
