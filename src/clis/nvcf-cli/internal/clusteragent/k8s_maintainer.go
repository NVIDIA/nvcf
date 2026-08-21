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
	maintenanceModeCordonAndDrain = "CordonAndDrain"

	// Namespace defaults applied when the NVCFBackend CR leaves them empty,
	// matching DefaultNVCASystemNamespace / DefaultNVCARequestsNamespace upstream.
	defaultSystemNamespace   = "nvca-system"
	defaultRequestsNamespace = "nvcf-backend"
)

// rolloutPollInterval bounds how often waitForRollout polls the Deployment. It
// is a var so tests can shorten it.
var rolloutPollInterval = 2 * time.Second

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
		result.Message = "already in the requested state; no change"
		return result, nil
	}
	result.RolloutTriggered = true

	if !opts.Force && opts.Timeout > 0 {
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
			// addFeatureFlagToConfig is idempotent: it returns cur unchanged
			// exactly when the flag is already present, so this doubles as a
			// membership check without a separate parser.
			hasFlag := addFeatureFlagToConfig(cur, cordonAndDrainFeatureFlag) == cur
			configReady = hasFlag == drain
		}

		rolloutReady := false
		deploy, err := m.cs.AppsV1().Deployments(systemNS).Get(ctx, nvcaDeploymentName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			rolloutReady = true
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

	result, err := m.killMatching(ctx, target, opts, func(fid, vid string) bool {
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
	return result, aggregateKillError(result)
}

// KillAll terminates every ICMSRequest on the cluster. An empty cluster returns
// an empty result and no error.
func (m *k8sMaintainer) KillAll(ctx context.Context, opts KillOptions) (*KillResult, error) {
	target, err := m.resolveAndVerify(ctx, opts.BackendNS, opts.ExpectClusterID)
	if err != nil {
		return nil, err
	}

	result, err := m.killMatching(ctx, target, opts, func(string, string) bool { return true })
	if err != nil {
		return nil, err
	}
	return result, aggregateKillError(result)
}

// killMatching lists ICMSRequests in the requests namespace, selects the ones
// the predicate accepts (in deterministic order), and deletes each unless DryRun.
// Scope is intentionally limited to target.RequestsNamespace: the NVCA operator
// contract guarantees all ICMSRequests for a cluster live in the single namespace
// recorded in the NVCFBackend CR's requestsNamespace field. The inspector's
// all-namespace scan is a visibility-only read path that tolerates stale state;
// kill operations use the authoritative namespace to avoid accidental cross-cluster deletions.
func (m *k8sMaintainer) killMatching(ctx context.Context, target *ClusterTarget, opts KillOptions, match func(functionID, versionID string) bool) (*KillResult, error) {
	items, err := listICMSRequests(ctx, m.dc, target.RequestsNamespace)
	if err != nil {
		return nil, err
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
			if err := m.deleteICMSRequest(ctx, killed.Namespace, killed.Name, opts.Force); err != nil {
				killed.Error = err.Error()
				result.FailedCount++
			} else {
				// Audit line for the termination, including the operator-supplied
				// reason. Carried in the result too, but this emits it to logs.
				logging.Info("terminated ICMSRequest %s/%s (function=%s version=%s) reason=%q",
					killed.Namespace, killed.Name, killed.FunctionID, killed.FunctionVersionID, opts.Reason)
			}
		}
		result.Affected = append(result.Affected, killed)
	}
	return result, nil
}

// deleteICMSRequest deletes one ICMSRequest. When force is set, it first strips
// finalizers so a CR stuck Terminating is removed even if NVCA is not running.
// A NotFound on delete is treated as success (the reconciler raced us).
func (m *k8sMaintainer) deleteICMSRequest(ctx context.Context, namespace, name string, force bool) error {
	if force {
		if err := m.stripFinalizers(ctx, namespace, name); err != nil {
			return err
		}
	}
	err := m.dc.Resource(icmsRequestGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
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

func aggregateKillError(result *KillResult) error {
	if result.FailedCount == 0 {
		return nil
	}
	return fmt.Errorf("failed to terminate %d of %d ICMSRequest(s)", result.FailedCount, len(result.Affected))
}

// --- agent-config YAML edits ---
//
// These mirror the line-based edits in nvca/pkg/operator/cleanup/cleanup.go so
// the CLI changes config.yaml exactly as the operator does, preserving the rest
// of the file. A structured re-marshal was rejected because it reorders keys and
// drops comments. Missing sections degrade to a no-op rather than corrupting the
// file.

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
