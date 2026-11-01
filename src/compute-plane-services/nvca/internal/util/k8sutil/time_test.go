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
	"reflect"
	"testing"
	"time"
)

func TestTimeConfigComplete(t *testing.T) {
	// Test case 1: All fields set to non-zero values
	tCfg := &TimeConfig{
		MaxRunningTimeout:                         10 * time.Minute,
		ModelCacheIdlePeriod:                      30 * time.Second,
		ModelCacheIdleCleanupPeriod:               1 * time.Second,
		ModelCacheROPVCBindTimeGracePeriod:        2 * time.Second,
		ModelCacheVolumeDetachmentTimeout:         3 * time.Second,
		WorkerDegradationTimeout:                  1 * time.Hour,
		WorkerStartupTimeout:                      2 * time.Hour,
		PodLaunchThresholdSecondsOnFailedRestarts: 10 * time.Second,
		PodLaunchThresholdMinutesOnInitFailure:    1 * time.Minute,
		PodScheduledThreshold:                     5 * time.Second,
		InitCacheJobFailureThreshold:              10 * time.Minute,
		MaxImagePullErrorThreshold:                1 * time.Second,
		NamespaceStuckTimeout:                     5 * time.Minute,
		FailingObjectsBackoffTimeout:              60 * time.Second,
		FailingObjectsBackoffRequeueInterval:      15 * time.Second,
	}

	tCfg.Complete()
	if len(zeroFields(tCfg)) != 0 {
		t.Errorf("Expected no zero fields, got %+q", zeroFields(tCfg))
	}

	// Test case 2: Some fields set to zero values, expecting defaults to be set
	tCfg = &TimeConfig{
		MaxRunningTimeout:                         0,
		ModelCacheIdlePeriod:                      0,
		ModelCacheIdleCleanupPeriod:               1 * time.Second,
		ModelCacheROPVCBindTimeGracePeriod:        0,
		ModelCacheVolumeDetachmentTimeout:         3 * time.Second,
		WorkerDegradationTimeout:                  1 * time.Hour,
		WorkerStartupTimeout:                      2 * time.Hour,
		PodLaunchThresholdSecondsOnFailedRestarts: 10 * time.Second,
		PodLaunchThresholdMinutesOnInitFailure:    0,
		PodScheduledThreshold:                     0,
		InitCacheJobFailureThreshold:              0,
		MaxImagePullErrorThreshold:                1 * time.Second,
		NamespaceStuckTimeout:                     0,
		FailingObjectsBackoffTimeout:              0,
		FailingObjectsBackoffRequeueInterval:      0,
	}

	tCfg.Complete()
	if len(zeroFields(tCfg)) != 0 {
		t.Errorf("Expected no zero fields, got %+q", zeroFields(tCfg))
	}
	if tCfg.MaxRunningTimeout != defaultMaxRunningTimeout {
		t.Errorf("Expected MaxRunningTimeout to be %v, got %v", defaultMaxRunningTimeout, tCfg.MaxRunningTimeout)
	}
	if tCfg.ModelCacheIdlePeriod != defaultModelCacheIdlePeriod {
		t.Errorf("Expected ModelCacheIdlePeriod to be %v, got %v", defaultModelCacheIdlePeriod, tCfg.ModelCacheIdlePeriod)
	}
	if tCfg.ModelCacheROPVCBindTimeGracePeriod != defaultModelCacheROPVCBindTimeGracePeriod {
		t.Errorf("Expected ModelCacheROPVCBindTimeGracePeriod to be %v, got %v", defaultModelCacheROPVCBindTimeGracePeriod, tCfg.ModelCacheROPVCBindTimeGracePeriod)
	}
	if tCfg.PodLaunchThresholdMinutesOnInitFailure != defaultPodLaunchThresholdMinutesOnInitFailure {
		t.Errorf("Expected PodLaunchThresholdMinutesOnInitFailure to be %v, got %v", defaultPodLaunchThresholdMinutesOnInitFailure, tCfg.PodLaunchThresholdMinutesOnInitFailure)
	}
	if tCfg.PodScheduledThreshold != defaultPodScheduledThreshold {
		t.Errorf("Expected PodScheduledThreshold to be %v, got %v", defaultPodScheduledThreshold, tCfg.PodScheduledThreshold)
	}
	if tCfg.InitCacheJobFailureThreshold != defaultInitCacheJobFailureThreshold {
		t.Errorf("Expected InitCacheJobFailureThreshold to be %v, got %v", defaultInitCacheJobFailureThreshold, tCfg.InitCacheJobFailureThreshold)
	}
	if tCfg.NamespaceStuckTimeout != defaultNamespaceStuckTimeout {
		t.Errorf("Expected NamespaceStuckTimeout to be %v, got %v", defaultNamespaceStuckTimeout, tCfg.NamespaceStuckTimeout)
	}
	if tCfg.FailingObjectsBackoffTimeout != defaultFailingObjectsBackoffTimeout {
		t.Errorf("Expected FailingObjectsBackoffTimeout to be %v, got %v", defaultFailingObjectsBackoffTimeout, tCfg.FailingObjectsBackoffTimeout)
	}
	if tCfg.FailingObjectsBackoffRequeueInterval != defaultFailingObjectsBackoffRequeueInterval {
		t.Errorf("Expected FailingObjectsBackoffRequeueInterval to be %v, got %v", defaultFailingObjectsBackoffRequeueInterval, tCfg.FailingObjectsBackoffRequeueInterval)
	}
}

func zeroFields(tCfg *TimeConfig) []string {
	var zeroFields []string
	v := reflect.ValueOf(tCfg).Elem()
	for i := range v.Type().NumField() {
		if v.Field(i).IsZero() {
			zeroFields = append(zeroFields, v.Type().Field(i).Name)
		}
	}
	return zeroFields
}
