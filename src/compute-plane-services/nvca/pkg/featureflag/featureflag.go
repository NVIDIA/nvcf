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

package featureflag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/urfave/cli/v2"
)

var (
	flags      = make(map[string]*FeatureFlag)
	flagsMutex sync.Mutex
)

var (
	LogPosting                    = newFeatureFlag("LogPosting", newBool(false))
	CachingSupport                = newFeatureFlag("CachingSupport", newBool(false))
	NVMeshEncryption              = newFeatureFlag("NVMeshEncryption", newBool(false))
	PeriodicInstanceStatusUpdate  = newFeatureFlag("PeriodicInstanceStatusUpdate", newBool(true))
	HelmRBACEnforcement           = newFeatureFlag("HelmRBACEnforcement", newBool(true))
	DynamicGPUDiscovery           = newFeatureFlag("DynamicGPUDiscovery", newBool(true))
	MultipleGPUTypesAllowed       = newFeatureFlag("MultipleGPUTypesAllowed", newBool(true))
	UniformInstanceLabels         = newFeatureFlag("UniformInstanceLabels", newBool(true))
	AutoPurgeDegradedWorkers      = newFeatureFlag("AutoPurgeDegradedWorkers", newBool(true))
	HelmPostRender                = newFeatureFlag("HelmPostRender", newBool(true))
	HelmSharedStorage             = newHelmSharedStorageFeatureFlag("HelmSharedStorage", true)
	ClusterTargeting              = newFeatureFlag("ClusterTargeting", newBool(true))
	HelmResourceConstraints       = newFeatureFlag("HelmResourceConstraints", newBool(true))
	HelmAllowCPUNodes             = newFeatureFlag("HelmAllowCPUNodes", newBool(false))
	BinPackTenantWorkloads        = newFeatureFlag("BinPackTenantWorkloads", newBool(false))
	GXCache                       = newFeatureFlag("GXCache", newBool(false))
	LowLatencyStreaming           = newFeatureFlag("LowLatencyStreaming", newBool(true))
	UseFunctionDeploymentStages   = newFeatureFlag("UseFunctionDeploymentStages", newBool(false))
	PVCRebind                     = newFeatureFlag("PVCRebind", newBool(false))
	HelmInternalPersistentStorage = newHelmInternalPersistentStorageFeatureFlag(false)
	MultiNodeWorkloads            = newFeatureFlag("MultiNodeWorkloads", newBool(true))
	BYOObservability              = newFeatureFlag("BYOObservability", newBool(false))
	BYOOFluentBit                 = newFeatureFlag("BYOOFluentBit", newBool(false))
	KAIScheduler                  = newFeatureFlag("KAIScheduler", newBool(false))
	HelmCustomAnnotations         = newFeatureFlag("HelmCustomAnnotations", newBool(false))
	MaxSQSBatchPull               = newFeatureFlag("MaxSQSBatchPull", newBool(true))
	CordonMaintenance             = newFeatureFlag("CordonMaintenance", newBool(false))
	CordonAndDrainMaintenance     = newFeatureFlag("CordonAndDrainMaintenance", newBool(false))
	// AckTaskRequestAfterPodsScheduled instructs the agent to only acknowledge ICMS requests with ICMS
	// and delete queue messages after all NVCT task pods have been accepted by the cluster's scheduler.
	AckTaskRequestAfterPodsScheduled = newFeatureFlag("AckTaskRequestAfterPodsScheduled", newBool(false))
	SelfHosted                       = newFeatureFlag("SelfHosted", newBool(false))
	// GracefulNoGPU allows NVCA to start and run without GPUs.
	// When enabled, NVCA will wait for GPUs to become available instead of failing,
	// and will pause queue processing if GPUs disappear during operation.
	GracefulNoGPU = newFeatureFlag("GracefulNoGPU", newBool(false))
	// MiniServiceRevisionHistory enables saving prior helm values as ConfigMaps on each MiniService's values update.
	MiniServiceRevisionHistory = newFeatureFlag("MiniServiceRevisionHistory", newBool(true))

	// AllowWorkloadKubernetesAPIAccess allows workload pods to access the Kubernetes API.
	// Required for First Class Operator (FCO) support.
	AllowWorkloadKubernetesAPIAccess = newFeatureFlag("AllowWorkloadKubernetesAPIAccess", newBool(false))

	// FCO operator-specific feature flags. To be removed once Phase 1 of the FCO SDD is in progress.
	DynamoOperatorSupport = newFeatureFlag("DynamoOperatorSupport", newBool(false))
)

// Feature flags for migrating resource limits.
var (
	// InfraResourceOverhead enables subtraction of infrastructure resource overhead
	// from instance type resources, potentially removing any instance type that cannot satisfy
	// infrastructure resources.
	//
	// Note: this will be enabled automatically if any Enforce*ResourceLimits flags are set.
	InfraResourceOverhead = newFeatureFlag("InfraResourceOverhead", newBool(false))
	// Enforces resource limits on helm functions via ResourceQuota's.
	// Sets pod.spec.{initContainers,containers}[*].resource.requests = limits
	EnforceHelmFunctionResourceLimits = newFeatureFlag("EnforceHelmFunctionResourceLimits", newBool(false))
	// Enforces resource limits on container functions via container resources.
	// Sets pod.spec.{initContainers,containers}[*].resource.requests = limits
	EnforceContainerFunctionResourceLimits = newFeatureFlag("EnforceContainerFunctionResourceLimits", newBool(false))
	// Enforces resource limits on helm tasks via container resources.
	// Sets pod.spec.{initContainers,containers}[*].resource.requests = limits
	EnforceHelmTaskResourceLimits = newFeatureFlag("EnforceHelmTaskResourceLimits", newBool(false))
	// Enforces resource limits on container tasks via container resources.
	// Sets pod.spec.{initContainers,containers}[*].resource.requests = limits
	EnforceContainerTaskResourceLimits = newFeatureFlag("EnforceContainerTaskResourceLimits", newBool(false))
)

// Deprecated feature flags
var (
	NVCA20                 = newFeatureFlag("NVCA2.0", newBool(true))
	UseFunctionTranslator  = newFeatureFlag("UseFunctionTranslator", newBool(true))
	deprecatedFeatureFlags = map[string]*FeatureFlag{
		NVCA20.Key:                 NVCA20,
		UseFunctionTranslator.Key:  UseFunctionTranslator,
		BinPackTenantWorkloads.Key: BinPackTenantWorkloads,
	}
)

var _ cli.Generic = (*CLIFlag)(nil)

type CLIFlag struct{}

func (s *CLIFlag) Set(value string) error {
	return parseFlags(value)
}

func (s *CLIFlag) String() string {
	flagsMutex.Lock()
	defer flagsMutex.Unlock()

	var sb strings.Builder

	keys := make([]string, len(flags))
	i := 0
	for _, f := range flags {
		keys[i] = f.Key
		i++
	}
	sort.Strings(keys)

	for i, k := range keys {
		f := flags[k]
		if f.Enabled() {
			sb.WriteByte('+')
		} else {
			sb.WriteByte('-')
		}
		sb.WriteString(f.Key)
		if i != len(keys)-1 {
			sb.WriteByte(',')
		}
	}

	return sb.String()
}

// FeatureFlag defines a feature flag
type FeatureFlag struct {
	Key          string
	enabled      *bool
	defaultValue *bool
}

// newFeatureFlag creates a newFeatureFlag feature flag
func newFeatureFlag(key string, defaultValue *bool) *FeatureFlag {
	flagsMutex.Lock()
	defer flagsMutex.Unlock()

	f := flags[key]
	if f == nil {
		f = &FeatureFlag{
			Key: key,
		}
		flags[key] = f
	}

	if f.defaultValue == nil {
		f.defaultValue = defaultValue
	}

	return f
}

// Enabled checks if the flag is enabled
func (f *FeatureFlag) Enabled() bool {
	if f.enabled != nil {
		return *f.enabled
	}
	if f.defaultValue != nil {
		return *f.defaultValue
	}
	return false
}

// Get returns given FeatureFlag.
func Get(flagName string) (*FeatureFlag, error) {
	flagsMutex.Lock()
	defer flagsMutex.Unlock()

	flag, found := flags[flagName]
	if !found {
		return nil, fmt.Errorf("flag %s not found", flagName)
	}
	return flag, nil
}

// newBool returns a pointer to the boolean value
func newBool(b bool) *bool {
	return &b
}

// parseFlags responsible for parse out the feature flag usage.
// Returns an error if invalid flag combinations are detected.
func parseFlags(f string) error {
	flagsMutex.Lock()
	defer flagsMutex.Unlock()

	log := core.GetLogger(core.WithDefaultLogger(context.Background()))

	for _, s := range strings.Split(strings.TrimSpace(f), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		enabled := true
		var ff *FeatureFlag
		if s[0] == '+' || s[0] == '-' {
			ff = flags[s[1:]]
			if s[0] == '-' {
				enabled = false
			}
		} else {
			ff = flags[s]
		}
		if ff != nil {
			// If it is a deprecated feature flag log and skip
			if _, ok := deprecatedFeatureFlags[ff.Key]; ok {
				log.Infof("FeatureFlag %s is deprecated, will be ignored, and set to default value %t", ff.Key, *ff.defaultValue)
				ff.enabled = ff.defaultValue
			} else {
				log.Infof("FeatureFlag %q=%v", ff.Key, enabled)
				ff.enabled = &enabled
			}
		} else {
			log.Warnf("Unknown FeatureFlag %q", s)
		}
	}

	// Post-process NGRE feature flags.
	if EnforceContainerFunctionResourceLimits.Enabled() ||
		EnforceContainerTaskResourceLimits.Enabled() ||
		EnforceHelmFunctionResourceLimits.Enabled() ||
		EnforceHelmTaskResourceLimits.Enabled() {
		InfraResourceOverhead.enabled = newBool(true)
		log.Infof("FeatureFlag %q=%v", InfraResourceOverhead.Key, *InfraResourceOverhead.enabled)
	}

	// HelmAllowCPUNodes is mutually exclusive with HelmResourceConstraints.
	if HelmAllowCPUNodes.Enabled() && HelmResourceConstraints.Enabled() {
		return fmt.Errorf("FeatureFlag %q cannot be enabled when %q is enabled",
			HelmAllowCPUNodes.Key, HelmResourceConstraints.Key)
	}

	if DynamoOperatorSupport.Enabled() && !AllowWorkloadKubernetesAPIAccess.Enabled() {
		AllowWorkloadKubernetesAPIAccess.enabled = newBool(true)
		log.Infof("FeatureFlag %q=%v (force-enabled when %q is enabled)",
			AllowWorkloadKubernetesAPIAccess.Key, *AllowWorkloadKubernetesAPIAccess.enabled, DynamoOperatorSupport.Key)
	}

	return nil
}
