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

package task

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/google/shlex"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/backendconfig"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/cache"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

// TODO: k8s version considerations.
// For now, all task Pod features are supported by all recent k8s versions

//nolint:gocyclo // complex function with many conditional branches
func translateContainer(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	tcfg.Default()

	if err := tcfg.ValidateContainer(); err != nil {
		return nil, fmt.Errorf("invalid container translate config: %v", err)
	}

	launchSpec := t.LaunchSpecification
	if launchSpec.ResultHandlingStrategy == "" {
		launchSpec.ResultHandlingStrategy = common.NoHandleResult
	}

	allEnvSet, err := common.DecodeEnvironmentB64(launchSpec.EnvironmentB64, common.EnvDecoderText)
	if err != nil {
		return nil, fmt.Errorf("decode worker environment: %v", err)
	}

	// telLaunchSpec may be nil if telemetries is not present.
	telLaunchSpec := launchSpec.Telemetries

	commonLabels, commonAnnotations := common.NewCommonMetadata(
		t.CreationQueueMessageMetadata, tcfg.TranslateConfig,
		launchSpec.ICMSEnvironment, common.MetadataOptions{
			TaskID:   t.Details.TaskID,
			TaskName: allEnvSet[common.TaskNameEncodedEnvKey],
		},
	)

	// Old instance type env var, which is instanceType "value".
	//nolint:staticcheck // SA1019: deprecated field used for backward compatibility
	if t.InstanceType != "" {
		allEnvSet[common.InstanceTypeEnvVarLegacy] = t.InstanceType //nolint:staticcheck // SA1019: deprecated field
	}
	// The new instance type env vars for both "name" and "value".
	if t.InstanceTypeName != "" && t.InstanceTypeValue != "" {
		allEnvSet[common.InstanceTypeNameEnvVar] = t.InstanceTypeName
		allEnvSet[common.InstanceTypeValueEnvVar] = t.InstanceTypeValue
	}

	allEnvSet[common.ICMSEnvironmentEnv] = launchSpec.ICMSEnvironment
	allEnvSet[common.CloudProviderEnvDep] = launchSpec.CloudProvider //nolint:staticcheck // SA1019: deprecated env var for backward compatibility
	allEnvSet[common.CloudProviderEnv] = launchSpec.CloudProvider
	allEnvSet[common.GPUNameEnv] = t.GPUType
	allEnvSet[common.GPUCountEnv] = strconv.Itoa(int(t.RequestedGPUCount)) //nolint:gosec // G115: intentional uint64->int conversion, values are bounded

	termGracePeriodSeconds := minimumTermGracePeriodSeconds
	if tgp := allEnvSet["TERMINATION_GRACE_PERIOD"]; tgp != "" {
		termGracePeriod, err := translateutil.ParseISO8601Duration(tgp)
		if err != nil {
			return nil, fmt.Errorf("parse worker env termination grace period: %v", err)
		}
		tgpSeconds := termGracePeriod.Seconds()
		if tgpSeconds > float64(minimumTermGracePeriodSeconds) {
			termGracePeriodSeconds = int64(tgpSeconds)
		}
	}

	// Extra utils envs.
	extraUtilsEnvs := append(common.MakeNVCTResultEnvs(),
		corev1.EnvVar{
			Name:  maxRunTimeEnvKey,
			Value: launchSpec.MaxRuntimeDuration,
		},
		corev1.EnvVar{
			Name:  resultHandlingStratEnvKey,
			Value: string(launchSpec.ResultHandlingStrategy),
		},
	)

	taskContainerImage := allEnvSet[common.ContainerTaskImageEnv]

	utilsContainerImage := allEnvSet[common.UtilsImageEnv]
	if tcfg.UtilsImage != "" {
		utilsContainerImage = tcfg.UtilsImage
	}
	initContainerImage := allEnvSet[common.InitImageEnv]
	if tcfg.InitImage != "" {
		initContainerImage = tcfg.InitImage
	}

	essContainerImage := allEnvSet[common.ESSAgentContainerEnv]
	hasSecretsAssertionToken := allEnvSet[common.SecretsAssertionTokenEnv] != ""
	hasTelemetries := telLaunchSpec != nil
	hasTaskSecrets := allEnvSet[common.TaskSecretsPresentEnv] == "true" ||
		(!hasTelemetries && hasSecretsAssertionToken)

	instanceIDBase := fmt.Sprintf("%s-task", tcfg.ObjectNameBase)

	// Volumes
	var volumes []corev1.Volume

	modelsVolume := corev1.Volume{
		Name: cache.ModelDataVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, modelsVolume)

	configDir := "/config/shared"
	configVolume := corev1.Volume{
		Name: "config-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, configVolume)

	taskVolume := corev1.Volume{
		Name: "task-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, taskVolume)

	dshmVolume := common.NewDevShmVolume()
	volumes = append(volumes, dshmVolume)

	instanceDir := common.NVCFInfoDir
	instanceVolume := common.NewInstanceDataVolume()
	volumes = append(volumes, instanceVolume)

	// Pull secrets
	var imagePullSecrets []corev1.LocalObjectReference

	workloadImagePullSecrets, err := common.ParseWorkloadImagePullSecrets(tcfg.ObjectNameBase, allEnvSet, false)
	if err != nil {
		return nil, err
	}
	for _, pullSecret := range workloadImagePullSecrets {
		objs = append(objs, pullSecret)
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: pullSecret.Name})
	}

	workerImagePullSecrets, err := common.ParseWorkerImagePullSecrets(tcfg.ObjectNameBase, allEnvSet)
	if err != nil {
		return nil, err
	}
	for _, pullSecret := range workerImagePullSecrets {
		objs = append(objs, pullSecret)
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: pullSecret.Name})
	}

	if cls := t.LaunchSpecification.CacheLaunchSpecification; cls != nil {
		cacheObjs, err := cache.Translate(cls, allEnvSet, workerImagePullSecrets, t.Details.TaskID)
		if err != nil {
			return nil, fmt.Errorf("translate cache objects: %w", err)
		}
		objs = append(objs, cacheObjs...)
	}

	// Parse task args, if any.
	var taskArgs []string
	if argsStr := allEnvSet[common.TaskContainerArgsEnv]; argsStr != "" {
		args, err := shlex.Split(argsStr)
		if err != nil {
			return nil, fmt.Errorf("parse container args: %v", err)
		}
		taskArgs = append(taskArgs, args...)
	}

	// Task envs with extra helper envs.
	taskEnvSet := map[string]string{}
	if envB64 := allEnvSet[common.TaskContainerEnvEnv]; envB64 != "" {
		taskEnvSet, err = common.DecodeEnvironmentB64(envB64, common.EnvDecoderJSON)
		if err != nil {
			return nil, fmt.Errorf("decode task environment: %v", err)
		}
	}
	taskEnvs := common.MapToEnv(taskEnvSet)
	taskEnvs, _ = common.MergeEnvs(taskEnvs, common.MakeWorkloadEnvVars(common.TaskCreationAction)...)
	taskEnvs = common.SortEnvs(taskEnvs)

	secretPath := common.TaskSecretPath(t.Details.TaskID)
	essSecretEnvs := common.NewESSSecretEnvs(secretPath)
	var essAccountsSecretEnvs []corev1.EnvVar
	if hasSecretsAssertionToken && hasTelemetries {
		essAccountsSecretEnvs, err = common.NewESSAccountsSecretEnvs(allEnvSet[common.NCAIDEncodedEnvKey], allEnvSet[common.SecretsAssertionTokenEnv], secretPath)
		if err != nil {
			return nil, fmt.Errorf("creating ESS account secret envs failed: %w", err)
		}
	}
	essDataVolumeMount := corev1.VolumeMount{
		Name:      common.EssDataVolumeName,
		MountPath: common.EssConfigDir,
	}
	secretDataVolumeMount := corev1.VolumeMount{
		Name:      common.EssSecretDataVolumeName,
		MountPath: common.EssSecretsDir,
	}
	secretDataVolumeMountRO := *secretDataVolumeMount.DeepCopy()
	secretDataVolumeMountRO.ReadOnly = true

	// To ensure names are < 64 chars, hash the base then use the first 16 bytes.
	instanceIDBaseSum := sha256.Sum256([]byte(instanceIDBase))
	appNameBase := "a" + hex.EncodeToString(instanceIDBaseSum[:])[:16]
	appName := fmt.Sprintf("%s-%s", appNameBase, common.ByooOTelCollectorPodNameBase)
	var otelCMName string
	if hasTelemetries {
		otelVersion := common.OTelVersion(allEnvSet[common.OTelContainerEnv])

		if otelVersion == "v1" {
			otelCMName = fmt.Sprintf("%s-%s", appNameBase, common.ByooOTelConfigCMNameBase)
			otelCM, err := common.NewOTelConfigMap(telLaunchSpec, backendconfig.TemplateConfig{
				BackendType:  backendconfig.K8s,
				WorkloadType: backendconfig.Container,
				Namespace:    tcfg.Namespace,
				TaskID:       t.Details.TaskID,
				InstanceID:   instanceIDBase,
				ZoneName:     tcfg.ClusterRegion,
			}, otelCMName)
			if err != nil {
				return nil, err
			}
			objs = append(objs, otelCM)
		}
	}

	// Pod instances
	instanceCount := int(t.InstanceCount) //nolint:gosec // G115: intentional uint64->int conversion, values are bounded
	if instanceCount == 0 {
		instanceCount = 1
	}
	for i := range instanceCount {
		instanceID := fmt.Sprintf("%d-%s", i, instanceIDBase)

		pod := &corev1.Pod{}
		pod.Name = instanceID
		pod.Spec = corev1.PodSpec{
			HostNetwork:                   tcfg.PodUseHostNetwork,
			DNSPolicy:                     tcfg.PodDNSPolicy,
			ImagePullSecrets:              imagePullSecrets,
			ServiceAccountName:            tcfg.ServiceAccountName,
			AutomountServiceAccountToken:  translateutil.NewBoolPtr(false),
			TerminationGracePeriodSeconds: new(int64),
			Volumes:                       volumes,
			Affinity:                      &corev1.Affinity{},
			RestartPolicy:                 corev1.RestartPolicyNever,
		}
		*pod.Spec.TerminationGracePeriodSeconds = termGracePeriodSeconds
		common.MergeTolerations(&pod.Spec, tcfg.Tolerations...)
		common.AddNVIDIAGPUNoScheduleToleration(&pod.Spec)

		instanceTypeValue := t.InstanceTypeValue
		if instanceTypeValue == "" {
			instanceTypeValue = t.InstanceType //nolint:staticcheck // SA1019: deprecated field used for backward compatibility
		}
		common.SetNodeAffinity(pod, tcfg.NodeAffinity, tcfg.InstanceTypeLabelSelectorKey, instanceTypeValue)
		common.SetPodAffinity(pod, tcfg.PodAffinity, tcfg.PodAntiAffinity)

		taskContainerVolumeMounts := append(cache.NewModelDataVolumeMounts(),
			corev1.VolumeMount{
				Name:      taskVolume.Name,
				MountPath: common.NVCTTaskDir,
			},
			corev1.VolumeMount{
				Name:      dshmVolume.Name,
				MountPath: common.DevShmPath,
			},
		)
		initContainerVolumeMounts := append(cache.NewModelDataVolumeMounts(),
			corev1.VolumeMount{
				Name:      configVolume.Name,
				MountPath: configDir,
			},
			corev1.VolumeMount{
				Name:      instanceVolume.Name,
				MountPath: instanceDir,
			},
		)
		utilsContainerVolumeMounts := []corev1.VolumeMount{
			{
				Name:      taskVolume.Name,
				MountPath: common.NVCTTaskDir,
			},
			{
				Name:      configVolume.Name,
				MountPath: configDir,
			},
			{
				Name:      instanceVolume.Name,
				MountPath: instanceDir,
			},
		}

		// Infra environment
		instanceIDEnv := corev1.EnvVar{
			Name:  "INSTANCE_ID",
			Value: instanceID,
		}
		initEnvs := common.MapToEnv(allEnvSet)
		initEnvs = append(initEnvs, instanceIDEnv)
		utilsEnvs := common.MapToEnv(allEnvSet)
		utilsEnvs = append(utilsEnvs, extraUtilsEnvs...)
		utilsEnvs = append(utilsEnvs, instanceIDEnv)

		if hasSecretsAssertionToken {
			essEnvs := []corev1.EnvVar{{
				Name:  common.EssAgentInitEnv,
				Value: "false",
			}}
			if hasTaskSecrets {
				essEnvs = append(essEnvs, essSecretEnvs...)
			}
			if hasTelemetries {
				essEnvs = append(essEnvs, essAccountsSecretEnvs...)
			}

			essContainer := common.NewESSContainer(essContainerImage, essEnvs)
			essContainer.VolumeMounts = []corev1.VolumeMount{
				secretDataVolumeMount,
				essDataVolumeMount,
			}
			pod.Spec.Containers = append(pod.Spec.Containers, essContainer)

			// Add volume mounts to containers
			taskContainerVolumeMounts = append(taskContainerVolumeMounts,
				secretDataVolumeMountRO)
			initContainerVolumeMounts = append(initContainerVolumeMounts,
				essDataVolumeMount)
			utilsContainerVolumeMounts = append(utilsContainerVolumeMounts,
				secretDataVolumeMountRO, essDataVolumeMount)

			// Add emptyDir ess and secret volumes
			pod.Spec.Volumes = append(pod.Spec.Volumes,
				corev1.Volume{
					Name: secretDataVolumeMount.Name,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				corev1.Volume{
					Name: essDataVolumeMount.Name,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			)
		}

		taskContainer := corev1.Container{
			Name:            taskContainerName,
			Image:           taskContainerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Args:            taskArgs,
			Env:             taskEnvs,
			Resources:       tcfg.WorkloadResources,
			SecurityContext: common.NewWorkloadContainerSecurityContext(),
			VolumeMounts:    taskContainerVolumeMounts,
		}

		pod.Spec.Containers = append(pod.Spec.Containers, taskContainer)

		// One init container for all resources and models,
		// which are fetch by init by task ID from the NVCT API.
		initContainer := corev1.Container{
			Name:            common.InitContainerName,
			Image:           initContainerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             common.SortEnvs(initEnvs),
			SecurityContext: common.NewInfraContainerSecurityContext(),
			VolumeMounts:    initContainerVolumeMounts,
		}
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, initContainer)

		// The ESS init container needs to be added after the init container,
		// since the init container creates config.hcl.
		if hasSecretsAssertionToken {
			essInitEnvs := []corev1.EnvVar{{
				Name:  common.EssAgentInitEnv,
				Value: "true",
			}}
			if hasTaskSecrets {
				essInitEnvs = append(essInitEnvs, essSecretEnvs...)
			}
			if hasTelemetries {
				essInitEnvs = append(essInitEnvs, essAccountsSecretEnvs...)
			}

			essInitContainer := common.NewESSInitContainer(essContainerImage, essInitEnvs)
			essInitContainer.VolumeMounts = []corev1.VolumeMount{
				secretDataVolumeMount,
				essDataVolumeMount,
			}
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, essInitContainer)
		}

		utilsContainer := corev1.Container{
			Name:            common.UtilsContainerName,
			Image:           utilsContainerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             common.SortEnvs(utilsEnvs),
			SecurityContext: common.NewInfraContainerSecurityContext(),
			VolumeMounts:    utilsContainerVolumeMounts,
		}
		// mutate startup / liveness / readiness probes
		common.MutateUtilsProbes(&utilsContainer)
		pod.Spec.Containers = append(pod.Spec.Containers, utilsContainer)

		// Setup telemetry for the pod
		if hasTelemetries {
			if err := common.SetupPodTelemetry(
				pod, telLaunchSpec, allEnvSet, common.WorkloadTypeContainer,
				tcfg.ClusterRegion, instanceID, tcfg.Namespace, common.TaskEnvVars(),
				appName, otelCMName, common.OTelCollectorBindAllAddresses, tcfg.OTelResources,
			); err != nil {
				return nil, err
			}
			fluentbitCM := common.SetupFluentBit(pod, telLaunchSpec, allEnvSet, common.WorkloadTypeContainer, tcfg.Namespace, tcfg.FluentbitEnabled, tcfg.FluentbitResources)
			if fluentbitCM != nil {
				objs = append(objs, fluentbitCM)
			}
		}

		objs = append(objs, pod)
	}

	// Set namespace, labels, and annotations for all objects.
	for _, obj := range objs {
		obj.SetNamespace(tcfg.Namespace)
		obj.SetLabels(translateutil.MergeMaps(obj.GetLabels(), commonLabels))
		obj.SetAnnotations(translateutil.MergeMaps(obj.GetAnnotations(), commonAnnotations))
		if err := translateutil.SetObjectGVK(obj); err != nil {
			return nil, err
		}
	}

	return objs, nil
}
