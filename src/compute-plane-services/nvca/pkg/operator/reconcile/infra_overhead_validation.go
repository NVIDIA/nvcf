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
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// maxListedWorkloads bounds how many workloads the overhead conflict error names per instance type.
const maxListedWorkloads = 3

// validateInfraOverheadForActiveInstances rejects an agent configuration whose
// infrastructure resource overhead leaves no room in an instance type that still
// has active instances. The agent leaves such an instance type out of its ICMS
// registration, and ICMS rejects removing an instance type with active
// instances, so the replacement agent could never register. Rejecting the
// configuration before the rollout keeps the running agent on its current
// configuration. Every rollout attempt repeats the check, so the change applies
// once those instances are gone or the overhead is lowered.
//
// The check repeats the agent's dynamic GPU discovery from the rendered agent
// configuration and the current nodes. agentEnvOverrides are the environment
// overrides the agent Deployment applies. When it cannot evaluate the
// configuration the way the agent would, it allows the rollout.
func (bc *BackendK8sCache) validateInfraOverheadForActiveInstances(
	ctx context.Context,
	agentEnvOverrides map[string]string,
	agentConfigCM *corev1.ConfigMap,
) error {
	log := core.GetLogger(ctx)

	if overridesOverheadEnvs(agentEnvOverrides) {
		log.Debug("Skipping the instance type overhead check: agent environment overrides change the overhead")
		return nil
	}
	cfg, err := nvcaconfig.DecodeConfig([]byte(agentConfigCM.Data[agentConfigFile]))
	if err != nil {
		log.WithError(err).Warn("Skipping the instance type overhead check: decode the agent configuration")
		return nil
	}
	k8sutil.SetConfigDefaultResourcesConfigOnly(&cfg)
	fetcher, err := featureflag.NewListFetcher(cfg.Agent.FeatureFlags, cfg.Cluster.Attributes)
	if err != nil {
		log.WithError(err).Warn("Skipping the instance type overhead check: evaluate the agent feature flags")
		return nil
	}
	// The agent registers static instance types without subtracting the overhead.
	if cfg.Agent.StaticGPUCapacity != 0 || !fetcher.IsFeatureFlagEnabled(featureflag.DynamicGPUDiscovery) {
		return nil
	}

	overhead, err := enforce.NewInfraOverheadGetter(fetcher, cfg,
		enforce.GetRuntimeClassK8sClient(bc.clients.K8s)).GetInfraOverhead(ctx)
	if err != nil {
		return skipOrRetryOverheadCheck(ctx, "get the infrastructure resource overhead", err)
	}
	if len(overhead) == 0 {
		return nil
	}

	removed, err := bc.instanceTypesRemovedByOverhead(ctx, cfg, fetcher, overhead)
	if err != nil || len(removed) == 0 {
		return err
	}
	active, err := bc.activeWorkloadsByInstanceType(ctx)
	if err != nil {
		return err
	}

	var conflicts []string
	exhaustedOverhead := corev1.ResourceList{}
	for _, it := range removed {
		workloads := active[it.Name]
		if len(workloads) == 0 {
			continue
		}
		detail := "active instances: " + formatWorkloads(workloads)
		if exhausted := exhaustedResources(it, overhead); len(exhausted) != 0 {
			detail = k8sutil.FormatResourceList(exhausted) + " before overhead; " + detail
			for rn := range exhausted {
				exhaustedOverhead[rn] = overhead[rn]
			}
		}
		conflicts = append(conflicts, fmt.Sprintf("%s (%s)", it.Name, detail))
	}
	if len(conflicts) == 0 {
		return nil
	}
	if len(exhaustedOverhead) == 0 {
		exhaustedOverhead = overhead
	}
	return &invalidAgentConfigError{err: fmt.Errorf(
		"infrastructure resource overhead of %s leaves no room in instance types with active instances: %s; "+
			"NVCA would drop them from its ICMS registration, which ICMS rejects while instances are active; "+
			"%s, or remove those instances first",
		k8sutil.FormatResourceList(overhead), strings.Join(conflicts, ", "), overheadRemedy(cfg, exhaustedOverhead))}
}

// overridesOverheadEnvs reports whether the agent environment sets variables
// that change the overhead in ways this check cannot reproduce.
func overridesOverheadEnvs(envs map[string]string) bool {
	for _, name := range append(k8sutil.DeprecatedResourceEnvs(), kata.RuntimeClassEnv, kata.RuntimeClassGPUEnv) {
		if envs[name] != "" {
			return true
		}
	}
	return false
}

// skipOrRetryOverheadCheck returns a transient err so the sync retries the
// check. Retrying cannot fix other errors, so it logs them and allows the
// rollout instead.
func skipOrRetryOverheadCheck(ctx context.Context, action string, err error) error {
	if k8sutil.IsTransientK8sError(err) {
		return fmt.Errorf("check the instance type overhead: %s: %w", action, err)
	}
	core.GetLogger(ctx).WithError(err).Warnf("Skipping the instance type overhead check: %s", action)
	return nil
}

func (bc *BackendK8sCache) instanceTypesRemovedByOverhead(
	ctx context.Context,
	cfg nvcaconfig.Config,
	fetcher featureflag.Fetcher,
	overhead corev1.ResourceList,
) ([]nvcatypes.RegistrationInstanceType, error) {
	log := core.GetLogger(ctx)

	provider, err := nvcatypes.NormalizeClusterProvider(cfg.Cluster.CloudProvider)
	if err != nil {
		log.WithError(err).Warn("Skipping the instance type overhead check: normalize the cluster provider")
		return nil, nil
	}
	nodes, err := bc.clients.K8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: nodefeatures.NodeLabelSelector(fetcher),
	})
	if err != nil {
		return nil, skipOrRetryOverheadCheck(ctx, "list nodes", err)
	}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for i := range nodes.Items {
		if err := indexer.Add(&nodes.Items[i]); err != nil {
			return nil, fmt.Errorf("index node %s: %w", nodes.Items[i].Name, err)
		}
	}

	// Shared-cluster node filtering is not repeated here. Counting more nodes can
	// only make an instance type look larger, so it cannot cause a false rejection.
	nfc := nodefeatures.NewDynamicClient(nil, listersv1.NewNodeLister(indexer), provider,
		nodefeatures.DynamicClientOptions{
			MultipleGPUTypesAllowed: fetcher.IsFeatureFlagEnabled(featureflag.MultipleGPUTypesAllowed),
			UniformInstanceLabels:   fetcher.IsFeatureFlagEnabled(featureflag.UniformInstanceLabels),
			AttributeFetcher:        fetcher,
		})
	backendGPUs, err := nfc.GetAllBackendGPUs(ctx)
	if err != nil {
		// No GPUs, or a GPU layout the agent rejects for another reason.
		log.WithError(err).Debug("Skipping the instance type overhead check: discover GPUs")
		return nil, nil
	}
	return nvcatypes.BackendGPUs(backendGPUs).InstanceTypesRemovedByOverhead(
		fetcher.IsFeatureFlagEnabled(featureflag.MultiNodeWorkloads), overhead), nil
}

// activeWorkloadsByInstanceType returns, for each instance type with instances
// that ICMS still counts as active, the workloads those instances belong to,
// mapped to their number of active instances.
func (bc *BackendK8sCache) activeWorkloadsByInstanceType(ctx context.Context) (map[string]map[string]int, error) {
	list, err := bc.clients.DynamicClient.Resource(nvcav2beta1.SchemeGroupVersion.WithResource("icmsrequests")).
		Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, skipOrRetryOverheadCheck(ctx, "list ICMS requests", err)
	}

	active := map[string]map[string]int{}
	for _, item := range list.Items {
		var req nvcav2beta1.ICMSRequest
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &req); err != nil {
			core.GetLogger(ctx).WithError(err).Warnf("Skipping ICMS request %s/%s in the instance type overhead check",
				item.GetNamespace(), item.GetName())
			continue
		}
		info := req.Spec.CreationMsgInfo
		instanceType := info.InstanceTypeName
		if instanceType == "" {
			instanceType = info.InstanceType //nolint:staticcheck // set alone by older ICMS versions
		}
		count := activeInstanceCount(&req)
		if instanceType == "" || count == 0 {
			continue
		}
		if active[instanceType] == nil {
			active[instanceType] = map[string]int{}
		}
		active[instanceType][workloadName(&req)] += count
	}
	return active, nil
}

// activeInstanceCount returns how many instances of req ICMS still counts as
// active: those whose termination NVCA has not reported yet. ICMS tracks the
// requested instances before NVCA records them, so a request without recorded
// instances counts with its requested instances until it fails.
func activeInstanceCount(req *nvcav2beta1.ICMSRequest) int {
	if len(req.Status.Instances) == 0 {
		if req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusFailureAcknowledged {
			return 0
		}
		return int(req.Spec.CreationMsgInfo.InstanceCount) //nolint:gosec // instance counts are small
	}
	count := 0
	for _, inst := range req.Status.Instances {
		if inst.LastReportedStatus != string(nvcatypes.ICMSInstanceTerminated) {
			count++
		}
	}
	return count
}

func workloadName(req *nvcav2beta1.ICMSRequest) string {
	switch {
	case req.Spec.FunctionDetails.FunctionID != "":
		return fmt.Sprintf("function %s version %s",
			req.Spec.FunctionDetails.FunctionID, req.Spec.FunctionDetails.FunctionVersionID)
	case req.Spec.TaskDetails.TaskID != "":
		return "task " + req.Spec.TaskDetails.TaskID
	default:
		return "request " + req.Spec.RequestID
	}
}

func formatWorkloads(workloads map[string]int) string {
	names := make([]string, 0, len(workloads))
	for name := range workloads {
		names = append(names, name)
	}
	sort.Strings(names)
	listed := make([]string, 0, maxListedWorkloads+1)
	for _, name := range names[:min(len(names), maxListedWorkloads)] {
		listed = append(listed, fmt.Sprintf("%s (%d)", name, workloads[name]))
	}
	if more := len(names) - maxListedWorkloads; more > 0 {
		listed = append(listed, fmt.Sprintf("%d more", more))
	}
	return strings.Join(listed, ", ")
}

// exhaustedResources returns the resources of it, before overhead, that overhead uses up.
func exhaustedResources(it nvcatypes.RegistrationInstanceType, overhead corev1.ResourceList) corev1.ResourceList {
	capacity := map[corev1.ResourceName]string{
		corev1.ResourceCPU:              it.CPU,
		corev1.ResourceMemory:           it.SystemMemory,
		corev1.ResourceEphemeralStorage: it.Storage,
	}
	exhausted := corev1.ResourceList{}
	for rn, oq := range overhead {
		if q, err := resource.ParseQuantity(capacity[rn]); err == nil && q.Cmp(oq) <= 0 {
			exhausted[rn] = q
		}
	}
	return exhausted
}

// overheadRemedy names the chart values that reserve the given resources,
// leading with byoo.additionalResourceOverhead when it reserves any of them.
func overheadRemedy(cfg nvcaconfig.Config, resources corev1.ResourceList) string {
	const infraContainerResources = "utils.resources, byoo.resources, byoo.fluentbit.resources, " +
		"storage.sharedStorage.server.resources"
	var additional []string
	for _, rn := range slices.Sorted(maps.Keys(resources)) {
		if q, ok := cfg.Agent.AdditionalResourceOverhead[rn]; ok && !q.IsZero() {
			additional = append(additional, fmt.Sprintf("byoo.additionalResourceOverhead.%s (%s)", rn, q.String()))
		}
	}
	if len(additional) == 0 {
		return "lower an infrastructure container resource (" + infraContainerResources +
			", byoo.additionalResourceOverhead)"
	}
	return fmt.Sprintf("lower %s or another infrastructure container resource (%s)",
		strings.Join(additional, ", "), infraContainerResources)
}
