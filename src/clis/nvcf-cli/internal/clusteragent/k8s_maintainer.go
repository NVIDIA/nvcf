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
	"strings"
	"time"

	"nvcf-cli/internal/logging"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// Maintenance constants. These mirror the NVCA operator contract defined in
// nvca/pkg/operator/cleanup/cleanup.go and pkg/operator/types/types.go. Drain
// flips CordonAndDrain maintenance on the agent-config ConfigMap and restarts
// the NVCA Deployment; the operator picks the change up on the rollout.
const (
	agentConfigConfigMapName      = "agent-config"
	agentConfigKey                = "config.yaml"
	nvcaDeploymentName            = "nvca"
	cordonAndDrainFeatureFlag     = "CordonAndDrainMaintenance"
	maintenanceModeCordonAndDrain = "CordonAndDrain"
	restartedAtAnnotation         = "kubectl.kubernetes.io/restartedAt"

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
	list, err := m.dc.Resource(nvcfBackendGVR).Namespace(backendNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, wrapCRDError(err, "NVCFBackend", backendNS)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no NVCFBackend resource found in namespace %q; is this context pointed at an NVCF compute-plane cluster (try --backend-namespace)?", backendNS)
	}

	obj := list.Items[0].Object
	return &ClusterTarget{
		ClusterID:         firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "clusterId"), nestedString(obj, "spec", "clusterConfig", "clusterID")),
		ClusterName:       nestedString(obj, "spec", "clusterConfig", "clusterName"),
		SystemNamespace:   firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "systemNamespace"), defaultSystemNamespace),
		RequestsNamespace: firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "requestsNamespace"), defaultRequestsNamespace),
	}, nil
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

	var transform func(string) string
	if drain {
		result.Mode = maintenanceModeCordonAndDrain
		transform = func(y string) string {
			return addMaintenanceModeToConfig(addFeatureFlagToConfig(y, cordonAndDrainFeatureFlag), maintenanceModeCordonAndDrain)
		}
	} else {
		transform = func(y string) string {
			return clearMaintenanceModeFromConfig(removeFeatureFlagFromConfig(y, cordonAndDrainFeatureFlag))
		}
	}

	if opts.DryRun {
		_, cur, err := m.getAgentConfig(ctx, systemNS)
		if err != nil {
			return nil, err
		}
		result.ConfigChanged = transform(cur) != cur
		if result.ConfigChanged {
			result.Message = "dry run: would update agent-config and restart NVCA"
		} else {
			result.Message = "dry run: already in the requested state; no change"
		}
		return result, nil
	}

	changed, err := m.patchAgentConfig(ctx, systemNS, transform)
	if err != nil {
		return nil, err
	}
	result.ConfigChanged = changed
	if !changed && !opts.Force {
		// Idempotent: skip the rollout so an in-flight drain is not disrupted.
		// Use --force to retry the restart if a previous run failed after the
		// config patch but before the rollout completed.
		result.Message = "already in the requested state; no change"
		return result, nil
	}

	if err := m.triggerRollout(ctx, systemNS); err != nil {
		return result, fmt.Errorf("agent-config updated but failed to restart NVCA: %w\n(re-run with --force to retry the restart)", err)
	}
	result.RolloutTriggered = true

	if !opts.Force && opts.Timeout > 0 {
		if err := m.waitForRollout(ctx, systemNS, opts.Timeout); err != nil {
			result.Message = fmt.Sprintf("agent-config updated and restart triggered, but the rollout did not complete in time: %v", err)
			return result, nil
		}
		result.RolloutComplete = true
	}
	return result, nil
}

// getAgentConfig fetches the agent-config ConfigMap and its config.yaml payload,
// translating common failures into actionable messages.
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

// patchAgentConfig reads, transforms, and writes config.yaml under retry-on-
// conflict. It reports whether the transform actually changed the content.
func (m *k8sMaintainer) patchAgentConfig(ctx context.Context, systemNS string, transform func(string) string) (bool, error) {
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, cur, err := m.getAgentConfig(ctx, systemNS)
		if err != nil {
			return err
		}
		next := transform(cur)
		if next == cur {
			changed = false
			return nil
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[agentConfigKey] = next
		if _, err := m.cs.CoreV1().ConfigMaps(systemNS).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// triggerRollout restarts the NVCA Deployment by stamping the standard restart
// annotation on its pod template, mirroring triggerNVCARollout in the operator.
func (m *k8sMaintainer) triggerRollout(ctx context.Context, systemNS string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := m.cs.AppsV1().Deployments(systemNS).Get(ctx, nvcaDeploymentName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("NVCA deployment %s/%s not found", systemNS, nvcaDeploymentName)
			}
			return fmt.Errorf("failed to get NVCA deployment %s/%s: %w", systemNS, nvcaDeploymentName, err)
		}
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[restartedAtAnnotation] = time.Now().UTC().Format(time.RFC3339)
		_, err = m.cs.AppsV1().Deployments(systemNS).Update(ctx, deploy, metav1.UpdateOptions{})
		return err
	})
}

// waitForRollout polls until the NVCA Deployment rollout completes or the
// timeout elapses, mirroring waitForDeploymentRollout in the operator.
func (m *k8sMaintainer) waitForRollout(ctx context.Context, systemNS string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		deploy, err := m.cs.AppsV1().Deployments(systemNS).Get(ctx, nvcaDeploymentName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get NVCA deployment %s/%s: %w", systemNS, nvcaDeploymentName, err)
		}

		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		// ObservedGeneration must catch up to the spec generation first, otherwise
		// the status still reflects the previous rollout and we could report
		// completion before the restart we just triggered has even begun.
		if deploy.Status.ObservedGeneration >= deploy.Generation &&
			deploy.Status.UpdatedReplicas == desired &&
			deploy.Status.AvailableReplicas == desired &&
			deploy.Status.UnavailableReplicas == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for NVCA deployment %s/%s rollout", systemNS, nvcaDeploymentName)
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

func removeFeatureFlagFromConfig(configYAML, featureFlag string) string {
	lines := strings.Split(configYAML, "\n")

	// Remove the flag only within the featureFlags: section.
	inFlags := false
	without := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "featureFlags:" {
			inFlags = true
		} else if inFlags && !strings.HasPrefix(trimmed, "- ") && trimmed != "" {
			inFlags = false
		}
		if inFlags && trimmed == "- "+featureFlag {
			continue
		}
		without = append(without, line)
	}

	// Drop an orphaned featureFlags: key whose list is now empty.
	out := make([]string, 0, len(without))
	for i, line := range without {
		if strings.TrimLeft(line, " \t") == "featureFlags:" {
			hasItem := false
			for j := i + 1; j < len(without); j++ {
				trimmed := strings.TrimLeft(without[j], " \t")
				if trimmed == "" {
					continue
				}
				hasItem = strings.HasPrefix(trimmed, "- ")
				break
			}
			if !hasItem {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func addMaintenanceModeToConfig(configYAML, maintenanceMode string) string {
	lines := strings.Split(configYAML, "\n")

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "maintenanceMode:") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "maintenanceMode: " + maintenanceMode
			return strings.Join(lines, "\n")
		}
	}

	for i, line := range lines {
		if strings.TrimRight(line, " \t\r") == "agent:" {
			lines = insertAfter(lines, i, "  maintenanceMode: "+maintenanceMode)
			break
		}
	}
	return strings.Join(lines, "\n")
}

func clearMaintenanceModeFromConfig(configYAML string) string {
	lines := strings.Split(configYAML, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "maintenanceMode:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func insertAfter(lines []string, index int, newLine string) []string {
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:index+1]...)
	result = append(result, newLine)
	result = append(result, lines[index+1:]...)
	return result
}
