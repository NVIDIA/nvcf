// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvcaconfig

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestConfig_Complete(t *testing.T) {
	t.Run("sets_default_environment", func(t *testing.T) {
		cfg := Config{}
		completed := cfg.Complete()
		assert.Equal(t, EnvironmentProduction, completed.Environment)
	})

	t.Run("preserves_set_environment", func(t *testing.T) {
		cfg := Config{Environment: EnvironmentStaging}
		completed := cfg.Complete()
		assert.Equal(t, EnvironmentStaging, completed.Environment)
	})

	t.Run("completes_nested_configs", func(t *testing.T) {
		cfg := Config{}
		completed := cfg.Complete()
		// Agent defaults
		assert.Equal(t, "info", completed.Agent.LogLevel)
		assert.Equal(t, defaultCredRenewInterval, completed.Agent.CredRenewInterval)
		// Workload defaults
		assert.Equal(t, defaultMaxRunningTimeout, completed.Workload.MaxRunningTimeout)
		// Authz defaults
		assert.Equal(t, uint64(3), completed.Authz.TokenFetchFailureThreshold)
	})
}

func TestAgentConfig_Complete(t *testing.T) {
	t.Run("sets_default_log_level", func(t *testing.T) {
		cfg := AgentConfig{}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "info", completed.LogLevel)
	})

	t.Run("preserves_set_log_level", func(t *testing.T) {
		cfg := AgentConfig{LogLevel: "debug"}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "debug", completed.LogLevel)
	})

	t.Run("completes_time_config", func(t *testing.T) {
		cfg := AgentConfig{}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, defaultCredRenewInterval, completed.CredRenewInterval)
		assert.Equal(t, defaultHeartbeatInterval, completed.HeartbeatInterval)
	})

	t.Run("leaves_ICMSURL_empty_when_not_set", func(t *testing.T) {
		cfg := AgentConfig{}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "", completed.ICMSURL)
	})

	t.Run("preserves_ICMSURL_when_set", func(t *testing.T) {
		cfg := AgentConfig{
			ICMSURL: "https://icms.example.com",
		}
		completed := cfg.Complete(EnvironmentProduction)
		assert.Equal(t, "https://icms.example.com", completed.ICMSURL)
	})
}

func TestAgentTimeConfig_Complete(t *testing.T) {
	t.Run("sets_all_defaults", func(t *testing.T) {
		cfg := AgentTimeConfig{}
		completed := cfg.Complete()
		assert.Equal(t, defaultCredRenewInterval, completed.CredRenewInterval)
		assert.Equal(t, defaultHeartbeatInterval, completed.HeartbeatInterval)
		assert.Equal(t, defaultSyncQueueInterval, completed.SyncQueueInterval)
		assert.Equal(t, defaultSyncRequestStatusInterval, completed.SyncRequestStatusInterval)
		assert.Equal(t, defaultSyncAcknowledgeRequestInterval, completed.SyncAcknowledgeRequestInterval)
		assert.Equal(t, defaultPeriodicInstanceStatusInterval, completed.PeriodicInstanceStatusInterval)
		assert.Equal(t, defaultRolloverServiceUpdateInterval, completed.RolloverServiceUpdateInterval)
		assert.Equal(t, defaultICMSRequestAckInterval, completed.ICMSRequestAckInterval)
		assert.Equal(t, defaultICMSRequestAckRetryTimeout, completed.ICMSRequestAckRetryTimeout)
	})

	t.Run("preserves_set_values", func(t *testing.T) {
		customInterval := 10 * time.Minute
		cfg := AgentTimeConfig{
			CredRenewInterval: customInterval,
		}
		completed := cfg.Complete()
		assert.Equal(t, customInterval, completed.CredRenewInterval)
		assert.Equal(t, defaultHeartbeatInterval, completed.HeartbeatInterval)
	})

	t.Run("uses_default_ICMSRequestAckInterval_when_not_set", func(t *testing.T) {
		cfg := AgentTimeConfig{}
		completed := cfg.Complete()
		assert.Equal(t, defaultICMSRequestAckInterval, completed.ICMSRequestAckInterval)
	})

	t.Run("preserves_ICMSRequestAckInterval_when_set", func(t *testing.T) {
		newValue := 25 * time.Minute
		cfg := AgentTimeConfig{
			ICMSRequestAckInterval: newValue,
		}
		completed := cfg.Complete()
		assert.Equal(t, newValue, completed.ICMSRequestAckInterval)
	})

	t.Run("uses_default_ICMSRequestAckRetryTimeout_when_not_set", func(t *testing.T) {
		cfg := AgentTimeConfig{}
		completed := cfg.Complete()
		assert.Equal(t, defaultICMSRequestAckRetryTimeout, completed.ICMSRequestAckRetryTimeout)
	})

	t.Run("preserves_ICMSRequestAckRetryTimeout_when_set", func(t *testing.T) {
		newValue := 12 * time.Minute
		cfg := AgentTimeConfig{
			ICMSRequestAckRetryTimeout: newValue,
		}
		completed := cfg.Complete()
		assert.Equal(t, newValue, completed.ICMSRequestAckRetryTimeout)
	})
}

func TestWorkloadConfig_Complete(t *testing.T) {
	t.Run("completes_time_config", func(t *testing.T) {
		cfg := WorkloadConfig{}
		completed := cfg.Complete()
		assert.Equal(t, defaultMaxRunningTimeout, completed.MaxRunningTimeout)
		assert.Equal(t, defaultModelCacheIdlePeriod, completed.ModelCacheIdlePeriod)
	})
}

func TestWorkloadTimeConfig_Complete(t *testing.T) {
	t.Run("sets_all_defaults", func(t *testing.T) {
		cfg := WorkloadTimeConfig{}
		completed := cfg.Complete()
		assert.Equal(t, defaultMaxRunningTimeout, completed.MaxRunningTimeout)
		assert.Equal(t, defaultModelCacheIdlePeriod, completed.ModelCacheIdlePeriod)
		assert.Equal(t, defaultModelCacheIdleCleanupPeriod, completed.ModelCacheIdleCleanupPeriod)
		assert.Equal(t, defaultModelCacheROPVCBindTimeGracePeriod, completed.ModelCacheROPVCBindTimeGracePeriod)
		assert.Equal(t, defaultModelCacheVolumeDetachmentTimeout, completed.ModelCacheVolumeDetachmentTimeout)
		assert.Equal(t, defaultWorkerDegradationTimeout, completed.WorkerDegradationTimeout)
		assert.Equal(t, defaultWorkerStartupTimeout, completed.WorkerStartupTimeout)
		assert.Equal(t, defaultPodLaunchThresholdSecondsOnFailedRestarts, completed.PodLaunchThresholdSecondsOnFailedRestarts)
		assert.Equal(t, defaultPodLaunchThresholdMinutesOnInitFailure, completed.PodLaunchThresholdMinutesOnInitFailure)
		assert.Equal(t, defaultPodScheduledThreshold, completed.PodScheduledThreshold)
		assert.Equal(t, defaultInitCacheJobFailureThreshold, completed.InitCacheJobFailureThreshold)
		assert.Equal(t, defaultMaxImagePullErrorThreshold, completed.MaxImagePullErrorThreshold)
		assert.Equal(t, defaultNamespaceStuckTimeout, completed.NamespaceStuckTimeout)
		assert.Equal(t, defaultFailingObjectsBackoffTimeout, completed.FailingObjectsBackoffTimeout)
		assert.Equal(t, defaultFailingObjectsBackoffRequeueInterval, completed.FailingObjectsBackoffRequeueInterval)
	})

	t.Run("preserves_set_values", func(t *testing.T) {
		customTimeout := 5 * time.Hour
		cfg := WorkloadTimeConfig{
			MaxRunningTimeout: customTimeout,
		}
		completed := cfg.Complete()
		assert.Equal(t, customTimeout, completed.MaxRunningTimeout)
		assert.Equal(t, defaultModelCacheIdlePeriod, completed.ModelCacheIdlePeriod)
	})
}

func TestAuthzConfig_Complete(t *testing.T) {
	t.Run("sets_default_threshold", func(t *testing.T) {
		cfg := AuthzConfig{}
		completed := cfg.Complete()
		assert.Equal(t, uint64(3), completed.TokenFetchFailureThreshold)
	})

	t.Run("preserves_set_threshold", func(t *testing.T) {
		cfg := AuthzConfig{TokenFetchFailureThreshold: 5}
		completed := cfg.Complete()
		assert.Equal(t, uint64(5), completed.TokenFetchFailureThreshold)
	})
}

func TestMaintenanceMode_String(t *testing.T) {
	tests := []struct {
		mode MaintenanceMode
		want string
	}{
		{MaintenanceModeNone, "None"},
		{MaintenanceModeCordon, "CordonOnly"},
		{MaintenanceModeCordonAndDrain, "CordonAndDrain"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.mode.String())
		})
	}
}

func TestEnvironment_Constants(t *testing.T) {
	assert.Equal(t, Environment("stg"), EnvironmentStaging)
	assert.Equal(t, Environment("prod"), EnvironmentProduction)
}

func TestNVCFClusterConfig(t *testing.T) {
	cfg := NVCFClusterConfig{
		Name:          "test-cluster",
		ID:            "cluster-id",
		GroupName:     "test-group",
		GroupID:       "group-id",
		NCAID:         "nca-id",
		Region:        "us-west-2",
		Attributes:    []string{"gpu", "nvlink"},
		CloudProvider: "aws",
	}
	assert.Equal(t, "test-cluster", cfg.Name)
	assert.Equal(t, "cluster-id", cfg.ID)
	assert.Equal(t, []string{"gpu", "nvlink"}, cfg.Attributes)
}

func TestOTELExporter_Constants(t *testing.T) {
	assert.Equal(t, OTELExporter(""), NoExporter)
	assert.Equal(t, OTELExporter("lightstep"), OTELExporter(LightstepExporter))
}

func TestValidationPolicyConfig_Validate(t *testing.T) {
	assert.Empty(t, ((*ValidationPolicyConfig)(nil)).Validate())
	assert.Empty(t, (&ValidationPolicyConfig{
		Name: "Default",
		AllowedExtraKubernetesTypes: []AllowedExtraKubernetesTypeConfig{{
			Group:    "foo.com",
			Version:  "v1",
			Kind:     "Foo",
			Resource: "foos",
		}},
	}).Validate())
	assert.Equal(t, []error{
		fmt.Errorf("name must be set"),
	}, (&ValidationPolicyConfig{}).Validate())
	assert.Equal(t, []error{
		fmt.Errorf("allowed extra kubernetes type 0 group must be set"),
		fmt.Errorf("allowed extra kubernetes type 0 version must be set"),
		fmt.Errorf("allowed extra kubernetes type 0 kind must be set"),
		fmt.Errorf("allowed extra kubernetes type 0 resource must be set"),
	}, (&ValidationPolicyConfig{
		Name:                        "Default",
		AllowedExtraKubernetesTypes: []AllowedExtraKubernetesTypeConfig{{}},
	}).Validate())
}

func TestResourceRequirements_DeepCopy(t *testing.T) {
	t.Run("handles_nil_receiver", func(t *testing.T) {
		var rr *ResourceRequirements
		copy := rr.DeepCopy()
		assert.Nil(t, copy)
	})

	t.Run("copies_empty_ResourceRequirements", func(t *testing.T) {
		rr := &ResourceRequirements{}
		copy := rr.DeepCopy()
		assert.NotNil(t, copy)
		assert.NotSame(t, rr, copy)
		assert.Empty(t, copy.Limits)
		assert.Empty(t, copy.Requests)
		assert.Nil(t, copy.Claims)
	})

	t.Run("deep_copies_Limits", func(t *testing.T) {
		limits := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		rr := &ResourceRequirements{
			Limits: limits,
		}
		copy := rr.DeepCopy()
		assert.NotSame(t, rr, copy)
		assert.Equal(t, limits, copy.Limits)

		// Modify the copy and verify original is unchanged
		copy.Limits[corev1.ResourceCPU] = resource.MustParse("4")
		assert.NotEqual(t, limits[corev1.ResourceCPU], copy.Limits[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("2"), rr.Limits[corev1.ResourceCPU])
	})

	t.Run("deep_copies_Requests", func(t *testing.T) {
		requests := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		}
		rr := &ResourceRequirements{
			Requests: requests,
		}
		copy := rr.DeepCopy()
		assert.NotSame(t, rr, copy)
		assert.Equal(t, requests, copy.Requests)

		// Modify the copy and verify original is unchanged
		copy.Requests[corev1.ResourceCPU] = resource.MustParse("3")
		assert.NotEqual(t, requests[corev1.ResourceCPU], copy.Requests[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("1"), rr.Requests[corev1.ResourceCPU])
	})

	t.Run("deep_copies_Claims", func(t *testing.T) {
		claims := []corev1.ResourceClaim{
			{Name: "claim1"},
			{Name: "claim2"},
		}
		rr := &ResourceRequirements{
			Claims: claims,
		}
		copy := rr.DeepCopy()
		assert.NotSame(t, rr, copy)
		assert.Equal(t, claims, copy.Claims)
		assert.Len(t, copy.Claims, len(claims))

		// Modify the copy and verify original is unchanged
		copy.Claims[0].Name = "modified-claim1"
		assert.Equal(t, "claim1", rr.Claims[0].Name)
		assert.Equal(t, "modified-claim1", copy.Claims[0].Name)
	})

	t.Run("handles_nil_Claims", func(t *testing.T) {
		rr := &ResourceRequirements{
			Limits: ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
			Claims: nil,
		}
		copy := rr.DeepCopy()
		assert.NotNil(t, copy)
		assert.Nil(t, copy.Claims)
	})

	t.Run("deep_copies_all_fields", func(t *testing.T) {
		limits := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		}
		requests := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		claims := []corev1.ResourceClaim{
			{Name: "gpu-claim"},
			{Name: "storage-claim"},
		}
		rr := &ResourceRequirements{
			Limits:   limits,
			Requests: requests,
			Claims:   claims,
		}
		copy := rr.DeepCopy()

		// Verify all fields are copied
		assert.Equal(t, limits, copy.Limits)
		assert.Equal(t, requests, copy.Requests)
		assert.Equal(t, claims, copy.Claims)

		// Verify they are deep copies (modifications don't affect original)
		copy.Limits[corev1.ResourceCPU] = resource.MustParse("8")
		copy.Requests[corev1.ResourceMemory] = resource.MustParse("16Gi")
		copy.Claims[0].Name = "modified-claim"

		assert.Equal(t, resource.MustParse("4"), rr.Limits[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("4Gi"), rr.Requests[corev1.ResourceMemory])
		assert.Equal(t, "gpu-claim", rr.Claims[0].Name)
	})
}

func TestResourceRequirements_ToK8sResourceRequirements(t *testing.T) {
	t.Run("converts_empty_ResourceRequirements", func(t *testing.T) {
		rr := &ResourceRequirements{}
		k8sRR := rr.ToK8sResourceRequirements()
		assert.Empty(t, k8sRR.Limits)
		assert.Empty(t, k8sRR.Requests)
		assert.Nil(t, k8sRR.Claims)
	})

	t.Run("converts_Limits", func(t *testing.T) {
		limits := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		rr := &ResourceRequirements{
			Limits: limits,
		}
		k8sRR := rr.ToK8sResourceRequirements()
		assert.Equal(t, corev1.ResourceList(limits), k8sRR.Limits)
		assert.Equal(t, resource.MustParse("2"), k8sRR.Limits[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("4Gi"), k8sRR.Limits[corev1.ResourceMemory])
		assert.Empty(t, k8sRR.Requests)
		assert.Nil(t, k8sRR.Claims)
	})

	t.Run("converts_Requests", func(t *testing.T) {
		requests := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		}
		rr := &ResourceRequirements{
			Requests: requests,
		}
		k8sRR := rr.ToK8sResourceRequirements()
		assert.Equal(t, corev1.ResourceList(requests), k8sRR.Requests)
		assert.Equal(t, resource.MustParse("1"), k8sRR.Requests[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("2Gi"), k8sRR.Requests[corev1.ResourceMemory])
		assert.Empty(t, k8sRR.Limits)
		assert.Nil(t, k8sRR.Claims)
	})

	t.Run("converts_Claims", func(t *testing.T) {
		claims := []corev1.ResourceClaim{
			{Name: "claim1"},
			{Name: "claim2"},
		}
		rr := &ResourceRequirements{
			Claims: claims,
		}
		k8sRR := rr.ToK8sResourceRequirements()
		assert.Equal(t, claims, k8sRR.Claims)
		assert.Len(t, k8sRR.Claims, 2)
		assert.Equal(t, "claim1", k8sRR.Claims[0].Name)
		assert.Equal(t, "claim2", k8sRR.Claims[1].Name)
		assert.Empty(t, k8sRR.Limits)
		assert.Empty(t, k8sRR.Requests)
	})

	t.Run("converts_all_fields", func(t *testing.T) {
		limits := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		}
		requests := ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		claims := []corev1.ResourceClaim{
			{Name: "gpu-claim"},
			{Name: "storage-claim"},
		}
		rr := &ResourceRequirements{
			Limits:   limits,
			Requests: requests,
			Claims:   claims,
		}
		k8sRR := rr.ToK8sResourceRequirements()

		// Verify Limits
		assert.Equal(t, corev1.ResourceList(limits), k8sRR.Limits)
		assert.Equal(t, resource.MustParse("4"), k8sRR.Limits[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("8Gi"), k8sRR.Limits[corev1.ResourceMemory])

		// Verify Requests
		assert.Equal(t, corev1.ResourceList(requests), k8sRR.Requests)
		assert.Equal(t, resource.MustParse("2"), k8sRR.Requests[corev1.ResourceCPU])
		assert.Equal(t, resource.MustParse("4Gi"), k8sRR.Requests[corev1.ResourceMemory])

		// Verify Claims
		assert.Equal(t, claims, k8sRR.Claims)
		assert.Len(t, k8sRR.Claims, 2)
		assert.Equal(t, "gpu-claim", k8sRR.Claims[0].Name)
		assert.Equal(t, "storage-claim", k8sRR.Claims[1].Name)
	})

	t.Run("handles_nil_Claims", func(t *testing.T) {
		rr := &ResourceRequirements{
			Limits: ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
			Claims: nil,
		}
		k8sRR := rr.ToK8sResourceRequirements()
		assert.NotEmpty(t, k8sRR.Limits)
		assert.Nil(t, k8sRR.Claims)
	})
}
