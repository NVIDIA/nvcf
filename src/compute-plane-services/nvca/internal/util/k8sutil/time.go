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
	"fmt"
	"reflect"
	"time"
)

const (
	defaultMaxRunningTimeout                         = 180 * time.Minute
	defaultModelCacheIdlePeriod                      = 1 * time.Hour
	defaultModelCacheIdleCleanupPeriod               = 5 * time.Minute
	defaultModelCacheROPVCBindTimeGracePeriod        = 2 * time.Minute
	defaultModelCacheVolumeDetachmentTimeout         = 5 * time.Minute
	defaultWorkerDegradationTimeout                  = 30 * time.Minute
	defaultWorkerStartupTimeout                      = 2 * time.Hour
	defaultPodLaunchThresholdSecondsOnFailedRestarts = 10 * time.Minute
	defaultPodLaunchThresholdMinutesOnInitFailure    = 2 * time.Hour
	defaultPodScheduledThreshold                     = 10 * time.Minute
	defaultInitCacheJobFailureThreshold              = 2 * defaultMaxRunningTimeout
	defaultMaxImagePullErrorThreshold                = 1 * time.Minute
	defaultNamespaceStuckTimeout                     = 5 * time.Minute
	defaultFailingObjectsBackoffTimeout              = 90 * time.Second
	defaultFailingObjectsBackoffRequeueInterval      = 30 * time.Second
)

type TimeConfig struct {
	// MaxRunningTimeout is the max duration an operation (ex. MiniService install, cache Job run) can run for before being considered failed.
	MaxRunningTimeout time.Duration
	// ModelCacheIdlePeriod is the max duration a model cache PV can exist while not in use.
	ModelCacheIdlePeriod time.Duration
	// ModelCacheIdleCleanupPeriod is the period of the model cache cleanup runner.
	ModelCacheIdleCleanupPeriod time.Duration
	// ModelCacheROPVCBindTimeGracePeriod is the max duration an RO PVC can be unbound during cache init.
	ModelCacheROPVCBindTimeGracePeriod time.Duration
	// ModelCacheVolumeDetachmentTimeout is the max duration to wait for PV detachment on cache cleanup.
	ModelCacheVolumeDetachmentTimeout time.Duration
	// defaultWorkerDegradationTimeout is the duration after which a Pod is considered degraded if its status indicates so.
	WorkerDegradationTimeout time.Duration
	// WorkerStartupTimeout is the duration after which a non-".status.phase=(Ready|Succeeded)" Pod is marked failed.
	WorkerStartupTimeout time.Duration
	// PodLaunchThresholdSecondsOnFailedRestarts is the duration after which a Pod with failed and restarting containers is marked failed.
	PodLaunchThresholdSecondsOnFailedRestarts time.Duration
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which a Pod with failed init containers is marked failed.
	PodLaunchThresholdMinutesOnInitFailure time.Duration
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which an un-scheduled Pod is marked failed.
	PodScheduledThreshold time.Duration
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which an init cache Job with unsuccessful Pods is marked failed.
	InitCacheJobFailureThreshold time.Duration
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which a Pod with container pull issues is marked failed.
	MaxImagePullErrorThreshold time.Duration
	// NamespaceStuckTimeout is the duration after which a terminating Namespace is considered stuck.
	NamespaceStuckTimeout time.Duration
	// FailingObjectsBackoffTimeout is the duration to retry transient events (FailedMount, FailedAttachVolume) before marking as failed.
	FailingObjectsBackoffTimeout time.Duration
	// FailingObjectsBackoffRequeueInterval is the interval to requeue reconciliation when objects are failing within the backoff period.
	FailingObjectsBackoffRequeueInterval time.Duration
}

// Complete sets defaults on TimeConfig. It panics if some field is not set or defaulted.
func (t *TimeConfig) Complete() *TimeConfig {
	if t.MaxRunningTimeout == 0 {
		t.MaxRunningTimeout = defaultMaxRunningTimeout
	}
	if t.ModelCacheIdlePeriod == 0 {
		t.ModelCacheIdlePeriod = defaultModelCacheIdlePeriod
	}
	if t.ModelCacheIdleCleanupPeriod == 0 {
		t.ModelCacheIdleCleanupPeriod = defaultModelCacheIdleCleanupPeriod
	}
	if t.ModelCacheROPVCBindTimeGracePeriod == 0 {
		t.ModelCacheROPVCBindTimeGracePeriod = defaultModelCacheROPVCBindTimeGracePeriod
	}
	if t.ModelCacheVolumeDetachmentTimeout == 0 {
		t.ModelCacheVolumeDetachmentTimeout = defaultModelCacheVolumeDetachmentTimeout
	}
	if t.WorkerDegradationTimeout == 0 {
		t.WorkerDegradationTimeout = defaultWorkerDegradationTimeout
	}
	if t.WorkerStartupTimeout == 0 {
		t.WorkerStartupTimeout = defaultWorkerStartupTimeout
	}
	if t.PodLaunchThresholdSecondsOnFailedRestarts == 0 {
		t.PodLaunchThresholdSecondsOnFailedRestarts = defaultPodLaunchThresholdSecondsOnFailedRestarts
	}
	if t.PodLaunchThresholdMinutesOnInitFailure == 0 {
		t.PodLaunchThresholdMinutesOnInitFailure = defaultPodLaunchThresholdMinutesOnInitFailure
	}
	if t.PodScheduledThreshold == 0 {
		t.PodScheduledThreshold = defaultPodScheduledThreshold
	}
	if t.InitCacheJobFailureThreshold == 0 {
		t.InitCacheJobFailureThreshold = defaultInitCacheJobFailureThreshold
	}
	if t.MaxImagePullErrorThreshold == 0 {
		t.MaxImagePullErrorThreshold = defaultMaxImagePullErrorThreshold
	}
	if t.NamespaceStuckTimeout == 0 {
		t.NamespaceStuckTimeout = defaultNamespaceStuckTimeout
	}
	if t.FailingObjectsBackoffTimeout == 0 {
		t.FailingObjectsBackoffTimeout = defaultFailingObjectsBackoffTimeout
	}
	if t.FailingObjectsBackoffRequeueInterval == 0 {
		t.FailingObjectsBackoffRequeueInterval = defaultFailingObjectsBackoffRequeueInterval
	}

	// Detect unset fields, which could cause downstream errors.
	var zeroFields []string
	v := reflect.ValueOf(t).Elem()
	for i := range v.Type().NumField() {
		if v.Field(i).IsZero() {
			zeroFields = append(zeroFields, v.Type().Field(i).Name)
		}
	}
	if len(zeroFields) != 0 {
		panic(fmt.Sprintf("code bug: TimeConfig fields not set: %+q", zeroFields))
	}

	return t
}
