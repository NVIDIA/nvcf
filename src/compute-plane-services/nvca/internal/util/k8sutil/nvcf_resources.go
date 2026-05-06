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

package k8sutil

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
)

const (
	// Base64-encoded corev1.ResourceList, extra resource overhead.
	//
	// Deprecated: use NVCA config
	additionalOverheadResourcesEnv = "NVCF_ADDITIONAL_OVERHEAD_RESOURCES_B64"

	// Utils/init resource limit envs.
	//
	// Deprecated: use NVCA config
	utilsCPULimitEnv              = "NVCF_UTILS_CPU_LIMIT"
	utilsMemoryLimitEnv           = "NVCF_UTILS_MEM_LIMIT"
	utilsEphemeralStorageLimitEnv = "NVCF_UTILS_EPHEMERAL_STORAGE_LIMIT"
)

var (
	defaultUtilsContainerResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}

	defaultSharedStorageServerContainerResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("20m"),
			corev1.ResourceMemory: resource.MustParse("150Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}

	defaultSharedStorageTaskDataStorageCapacity = resource.MustParse("100Gi")
)

func SetConfigDefaultResources(cfg *nvcaconfig.Config) error {
	// Utils resources, merged with legacy env.
	cfg.Agent.UtilsResources = nvcaconfig.ResourceList(overrideResources(
		corev1.ResourceList(cfg.Agent.UtilsResources),
		getUtilsContainerResourcesFromEnvs(os.Exit),
	))
	if len(cfg.Agent.UtilsResources) == 0 {
		cfg.Agent.UtilsResources = nvcaconfig.ResourceList(defaultUtilsContainerResources.Limits.DeepCopy())
	} else {
		for rn, rq := range defaultUtilsContainerResources.Limits {
			if _, ok := cfg.Agent.UtilsResources[rn]; !ok {
				cfg.Agent.UtilsResources[rn] = rq.DeepCopy()
			}
		}
	}

	// Only override defaults if the shared storage legacy env is set and populated.
	sharedStorageFFConfig, err := featureflag.ParseHelmSharedStorageSpecFromEnv()
	if err != nil {
		return err
	}
	if sc := sharedStorageFFConfig.Server; sc != nil && sc.SMBServerContainerResources != nil {
		cfg.Agent.SharedStorage.Server.ContainerResources.Requests = nvcaconfig.ResourceList(overrideResources(
			corev1.ResourceList(cfg.Agent.SharedStorage.Server.ContainerResources.Requests),
			sc.SMBServerContainerResources.Requests,
		))
		cfg.Agent.SharedStorage.Server.ContainerResources.Limits = nvcaconfig.ResourceList(overrideResources(
			corev1.ResourceList(cfg.Agent.SharedStorage.Server.ContainerResources.Limits),
			sc.SMBServerContainerResources.Limits,
		))
	}
	if len(cfg.Agent.SharedStorage.Server.ContainerResources.Requests) == 0 {
		cfg.Agent.SharedStorage.Server.ContainerResources.Requests =
			nvcaconfig.ResourceList(defaultSharedStorageServerContainerResources.Requests.DeepCopy())
	} else {
		reqs := cfg.Agent.SharedStorage.Server.ContainerResources.Requests
		for rn, rq := range defaultSharedStorageServerContainerResources.Requests {
			if _, ok := reqs[rn]; !ok {
				reqs[rn] = rq.DeepCopy()
			}
		}
	}
	if len(cfg.Agent.SharedStorage.Server.ContainerResources.Limits) == 0 {
		cfg.Agent.SharedStorage.Server.ContainerResources.Limits =
			nvcaconfig.ResourceList(defaultSharedStorageServerContainerResources.Limits.DeepCopy())
	} else {
		lims := cfg.Agent.SharedStorage.Server.ContainerResources.Limits
		for rn, rq := range defaultSharedStorageServerContainerResources.Limits {
			if _, ok := lims[rn]; !ok {
				lims[rn] = rq.DeepCopy()
			}
		}
	}

	// Task data for shared storage.
	if td := sharedStorageFFConfig.TaskData; td != nil {
		if td.StorageClassName != nil {
			cfg.Agent.SharedStorage.TaskData.StorageClassName = td.StorageClassName
		}
		if len(td.PVMountOptions) != 0 {
			cfg.Agent.SharedStorage.TaskData.PVMountOptions = td.PVMountOptions
		}
		if !td.Size.IsZero() {
			cfg.Agent.SharedStorage.TaskData.StorageCapacity = td.Size
		}
	}
	if cfg.Agent.SharedStorage.TaskData.StorageCapacity.IsZero() {
		cfg.Agent.SharedStorage.TaskData.StorageCapacity = defaultSharedStorageTaskDataStorageCapacity
	}

	// BYOO resources configuration.
	// Use the translate library defaults as a single source of truth.
	setDefaultResourceRequirements(&cfg.Agent.BYOOResources, common.GetDefaultContainerResourcesBYOO())

	// FluentBit resources configuration.
	// Use the translate library defaults as a single source of truth.
	setDefaultResourceRequirements(&cfg.Agent.BYOOFluentBitResources, common.GetDefaultContainerResourcesFluentbit())

	// Additional resource overhead, merged with legacy env.
	cfg.Agent.AdditionalResourceOverhead = nvcaconfig.ResourceList(overrideResources(
		corev1.ResourceList(cfg.Agent.AdditionalResourceOverhead),
		getAdditionalOverheadResourcesFromEnvs(os.Exit),
	))

	return nil
}

// setDefaultResourceRequirements sets default resource requirements if not configured.
func setDefaultResourceRequirements(resources *nvcaconfig.ResourceRequirements, defaults corev1.ResourceRequirements) {
	if len(resources.Requests) == 0 {
		resources.Requests = make(nvcaconfig.ResourceList)
		for rn, rq := range defaults.Requests {
			// Use MustParse to preserve string representation
			resources.Requests[rn] = resource.MustParse(rq.String())
		}
	} else {
		for rn, rq := range defaults.Requests {
			if _, ok := resources.Requests[rn]; !ok {
				resources.Requests[rn] = resource.MustParse(rq.String())
			}
		}
	}
	if len(resources.Limits) == 0 {
		resources.Limits = make(nvcaconfig.ResourceList)
		for rn, rq := range defaults.Limits {
			// Use MustParse to preserve string representation
			resources.Limits[rn] = resource.MustParse(rq.String())
		}
	} else {
		for rn, rq := range defaults.Limits {
			if _, ok := resources.Limits[rn]; !ok {
				resources.Limits[rn] = resource.MustParse(rq.String())
			}
		}
	}
}

func overrideResources(resources, resourcesFromEnvs corev1.ResourceList) corev1.ResourceList {
	if resources == nil {
		resources = corev1.ResourceList{}
	} else {
		resources = resources.DeepCopy()
	}
	if len(resourcesFromEnvs) == 0 {
		return resources
	}
	for rn := range resources {
		if rqe, ok := resourcesFromEnvs[rn]; ok {
			resources[rn] = rqe.DeepCopy()
		}
	}
	for rn, rqe := range resourcesFromEnvs {
		if _, ok := resources[rn]; !ok {
			resources[rn] = rqe.DeepCopy()
		}
	}
	return resources
}

var (
	parseAddlOverheadResourcesOnce sync.Once
	addlOverheadResources          corev1.ResourceList
)

// getAdditionalOverheadResourcesFromEnvs returns additional overhead resources, if any.
func getAdditionalOverheadResourcesFromEnvs(exit func(int)) corev1.ResourceList {
	// Lazily init.
	var err error
	parseAddlOverheadResourcesOnce.Do(func() {
		addlOverheadResources, err = initAdditionalOverheadResourcesFromEnvs(os.Getenv)
	})
	if err != nil {
		exit(1)
	}
	return addlOverheadResources.DeepCopy()
}

func initAdditionalOverheadResourcesFromEnvs(getenv getenvFunc) (corev1.ResourceList, error) {
	log := logrus.StandardLogger()

	overhead := corev1.ResourceList{}

	resourcesB64 := getenv(additionalOverheadResourcesEnv)
	if resourcesB64 == "" {
		return overhead, nil
	}

	resourcesBytes, err := base64.StdEncoding.DecodeString(resourcesB64)
	if err != nil {
		log.WithError(err).Errorf("Failed to parse env var '%s=%s', ignoring additional resource overhead",
			additionalOverheadResourcesEnv, resourcesB64)
		return nil, err
	}

	if err := json.Unmarshal(resourcesBytes, &overhead); err != nil {
		log.WithError(err).Errorf("Failed to decode '%s' JSON, ignoring additional resource overhead",
			string(resourcesBytes))
		return nil, err
	}

	return overhead, nil
}

// GetContainerResourcesBYOO returns the BYOO container resources from config.
// If no resources are configured, it returns the default hardcoded values for backward compatibility.
func GetContainerResourcesBYOO(cfg nvcaconfig.Config) corev1.ResourceRequirements {
	// Use configured resources if available
	if len(cfg.Agent.BYOOResources.Requests) > 0 || len(cfg.Agent.BYOOResources.Limits) > 0 {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList(cfg.Agent.BYOOResources.Requests).DeepCopy(),
			Limits:   corev1.ResourceList(cfg.Agent.BYOOResources.Limits).DeepCopy(),
		}
	}
	// Fallback to hardcoded values from vendor library
	return common.GetDefaultContainerResourcesBYOO()
}

// GetContainerResourcesFluentBit returns the FluentBit container resources from config.
// If no resources are configured, it returns the default hardcoded values for backward compatibility.
func GetContainerResourcesFluentBit(cfg nvcaconfig.Config) corev1.ResourceRequirements {
	// Use configured resources if available
	if len(cfg.Agent.BYOOFluentBitResources.Requests) > 0 || len(cfg.Agent.BYOOFluentBitResources.Limits) > 0 {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList(cfg.Agent.BYOOFluentBitResources.Requests).DeepCopy(),
			Limits:   corev1.ResourceList(cfg.Agent.BYOOFluentBitResources.Limits).DeepCopy(),
		}
	}
	// Fallback to hardcoded values from vendor library
	return common.GetDefaultContainerResourcesFluentbit()
}

// GetInfraContainerResourceOverhead returns the exact amount of resources consumed by
// all possible infra non-init container resources, including feature-flagged resources like FluentBit.
// Resources for the complete set of containers is used to maintain consistency across
// various request configurations, since this is not knowable in advance.
func GetInfraContainerResourceOverhead(
	cfg nvcaconfig.Config, fff featureFlagChecker, addlResourceLimits ...corev1.ResourceList,
) corev1.ResourceList {
	essResources := common.GetContainerResourcesESS()

	rls := []corev1.ResourceList{
		essResources.Limits,
		corev1.ResourceList(cfg.Agent.UtilsResources).DeepCopy(),
		corev1.ResourceList(cfg.Agent.SharedStorage.Server.ContainerResources.Limits).DeepCopy(),
		corev1.ResourceList(cfg.Agent.AdditionalResourceOverhead).DeepCopy(),
	}

	// Include BYOO OTel resources if the feature flag is enabled
	if fff != nil && fff.IsFeatureFlagEnabled(featureflag.BYOObservability) {
		byooResources := GetContainerResourcesBYOO(cfg)
		rls = append(rls, byooResources.Limits)
	}

	// Include FluentBit resources if the feature flag is enabled
	if fff != nil && fff.IsFeatureFlagEnabled(featureflag.BYOOFluentBit) {
		fluentbitResources := GetContainerResourcesFluentBit(cfg)
		rls = append(rls, fluentbitResources.Limits)
	}

	rls = append(rls, addlResourceLimits...)
	return combineResourceLists(rls...)
}

// featureFlagChecker is an interface for checking feature flags.
type featureFlagChecker interface {
	IsFeatureFlagEnabled(*featureflag.FeatureFlag) bool
}

func combineResourceLists(rls ...corev1.ResourceList) corev1.ResourceList {
	out := corev1.ResourceList{}
	for _, rl := range rls {
		for rn, rq := range rl {
			if _, ok := out[rn]; !ok {
				out[rn] = rq.DeepCopy()
				continue
			}
			existinRQ := out[rn]
			existinRQ.Add(rq)
			out[rn] = existinRQ
		}
	}
	return out
}

// getUtilsContainerResourcesFromEnvs returns utils container resource limit overrides, from envs.
func getUtilsContainerResourcesFromEnvs(exit func(int)) corev1.ResourceList {
	// Lazily init.
	var err error
	parseUtilsLimitsOnce.Do(func() {
		resourcesUtils, err = initUtilsContainerResourcesFromEnvs(os.Getenv)
	})
	if err != nil {
		exit(1)
	}
	return resourcesUtils.DeepCopy()
}

var (
	parseUtilsLimitsOnce sync.Once
	resourcesUtils       corev1.ResourceList
)

func initUtilsContainerResourcesFromEnvs(getenv getenvFunc) (corev1.ResourceList, error) {
	log := logrus.StandardLogger()

	limits := corev1.ResourceList{}
	for rk, v := range map[corev1.ResourceName]struct {
		envName string
	}{
		corev1.ResourceCPU: {
			envName: utilsCPULimitEnv,
		},
		corev1.ResourceMemory: {
			envName: utilsMemoryLimitEnv,
		},
		corev1.ResourceEphemeralStorage: {
			envName: utilsEphemeralStorageLimitEnv,
		},
	} {
		var limit resource.Quantity
		if limitStr := getenv(v.envName); limitStr != "" {
			parsedLimit, err := resource.ParseQuantity(limitStr)
			if err != nil {
				log.WithError(err).Errorf("Failed to parse env var '%s=%s'", v.envName, limitStr)
				return nil, err
			}
			limit = parsedLimit
		}
		if !limit.IsZero() {
			limits[rk] = limit
		}
	}
	return limits, nil
}

type getenvFunc func(key string) string

// SetNVCFInfraContainerResources sets utils/init container resources.
func SetNVCFInfraContainerResources(utilsResources corev1.ResourceList, pod *corev1.Pod) {
	for i, c := range pod.Spec.InitContainers {
		if c.Name == common.InitContainerName {
			// TODO: for failed caching, this needs to be dynamic so model download
			// is not throttled unnecessarily.
			pod.Spec.InitContainers[i].Resources.Requests = utilsResources.DeepCopy()
			pod.Spec.InitContainers[i].Resources.Limits = utilsResources.DeepCopy()
			break
		}
	}
	for i, c := range pod.Spec.Containers {
		if c.Name == common.UtilsContainerName {
			pod.Spec.Containers[i].Resources.Requests = utilsResources.DeepCopy()
			pod.Spec.Containers[i].Resources.Limits = utilsResources.DeepCopy()
			break
		}
	}
}

func EnsureResourceQuotas(
	ctx context.Context,
	fff featureflag.Fetcher,
	cs ClientShim,
	requestAction common.MessageAction,
	namespace string,
	computeReqs, computeLims corev1.ResourceList,
) error {
	log := core.GetLogger(ctx).WithField("namespace", namespace)

	computeReqs, computeLims = computeReqs.DeepCopy(), computeLims.DeepCopy()

	rqs := []*corev1.ResourceQuota{}

	// Parse GPU resources separately, as they should be created for all callers.
	gpuResName := nodefeatures.GetGPUResourceNameFetcher(fff)
	gpuRes := corev1.ResourceList{}
	if gpuReqs, ok := computeReqs[gpuResName]; ok {
		gpuRes[corev1.ResourceName("requests."+string(gpuResName))] = gpuReqs
		delete(computeReqs, gpuResName)
	}
	if gpuLims, ok := computeLims[gpuResName]; ok {
		gpuRes[corev1.ResourceName("limits."+string(gpuResName))] = gpuLims
		delete(computeLims, gpuResName)
	}

	isTask := requestAction == common.TaskCreationAction || requestAction == common.RequestICMSInstancesForTask
	isFunction := requestAction == common.FunctionCreationAction || requestAction == common.RequestICMSInstances
	if len(gpuRes) != 0 && fff.IsFeatureFlagEnabled(featureflag.HelmResourceConstraints) {
		rqs = append(rqs,
			&corev1.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "max-gpus",
					Namespace: namespace,
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: gpuRes,
				},
			},
		)
	}

	if (isFunction && fff.IsFeatureFlagEnabled(featureflag.EnforceHelmFunctionResourceLimits)) ||
		(isTask && fff.IsFeatureFlagEnabled(featureflag.EnforceHelmTaskResourceLimits)) {
		// Reformat resource names for the quota.
		hardComputeRes := make(corev1.ResourceList, len(computeReqs))
		for key, val := range computeReqs {
			hardComputeRes[corev1.ResourceName("requests."+string(key))] = val
		}
		for key, val := range computeLims {
			hardComputeRes[corev1.ResourceName("limits."+string(key))] = val
		}

		rqs = append(rqs,
			&corev1.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "max-resources",
					Namespace: namespace,
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: hardComputeRes,
				},
			},
			&corev1.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "max-objects",
					Namespace: namespace,
				},
				Spec: corev1.ResourceQuotaSpec{
					Hard: corev1.ResourceList{
						"configmaps":             *resource.NewQuantity(20, resource.DecimalSI),
						"secrets":                *resource.NewQuantity(20, resource.DecimalSI),
						"services":               *resource.NewQuantity(20, resource.DecimalSI),
						"pods":                   *resource.NewQuantity(100, resource.DecimalSI),
						"count/jobs.batch":       *resource.NewQuantity(10, resource.DecimalSI),
						"count/cronjobs.batch":   *resource.NewQuantity(10, resource.DecimalSI),
						"count/deployments.apps": *resource.NewQuantity(10, resource.DecimalSI),
						// Both deployments (which create replicasets) and statefulsets have revision history limits
						// of 10 by default; if the user does not set this to a lower value explicitly,
						// replicasets created by deployments and statefulsets that are repeatedly failing
						// may be admission-rejected by the RQ, covering up the true error with the misleading rejection error.
						// Bumping limits for both to 11 (10 historical, 1 active) should result in the correct
						// failure handling logic being used and an indicative message should be returned.
						"count/replicasets.apps":  *resource.NewQuantity(11, resource.DecimalSI),
						"count/statefulsets.apps": *resource.NewQuantity(11, resource.DecimalSI),
					},
				},
			},
		)
	}

	for _, rq := range rqs {
		log := log.WithField("resourcequota", rq.Name)

		if existingRQ, err := cs.Get(ctx, rq); err == nil {
			// Clear status to avoid generating status patch.
			existingRQ.(*corev1.ResourceQuota).Status = corev1.ResourceQuotaStatus{}
			patched, err := cs.Patch(ctx, existingRQ, rq)
			if err != nil {
				log.WithError(err).Error("Failed to patch enforcement ResourceQuota")
				return err
			}
			if patched {
				log.Debug("Patched Helm ResourceQuota")
			}
		} else if apierrors.IsNotFound(err) {
			if err := cs.Create(ctx, rq); err != nil {
				log.WithError(err).Error("Failed to create enforcement ResourceQuota")
				return err
			}
			log.Debug("Created Helm ResourceQuota")
		} else {
			log.WithError(err).Error("Failed to get enforcement ResourceQuota")
			return err
		}
	}

	return nil
}
