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

package function

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/backendconfig"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/cache"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

//nolint:gocyclo // complex function with many conditional branches
func translateHelmChart(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	// Fail if LLS is enabled for utils pod as a separate pod from the inference pod
	if t.Details.FunctionType == FunctionTypeStreaming {
		return nil, fmt.Errorf("LLS is not supported for Helm functions")
	}

	isLLM := t.Details.FunctionType == FunctionTypeLLM

	if err := tcfg.ValidateHelmChart(); err != nil {
		return nil, fmt.Errorf("invalid helm chart translate config: %v", err)
	}

	launchSpec := t.LaunchSpecification

	// telLaunchSpec may be nil if telemetries is not present.
	telLaunchSpec := launchSpec.Telemetries

	commonLabels, commonAnnotations := common.NewCommonMetadata(
		t.CreationQueueMessageMetadata, tcfg.TranslateConfig,
		launchSpec.ICMSEnvironment, common.MetadataOptions{
			FunctionID:        t.Details.FunctionID,
			FunctionVersionID: t.Details.FunctionVersionID,
		},
	)

	allEnvSet, err := common.DecodeEnvironmentB64(launchSpec.EnvironmentB64, common.EnvDecoderText)
	if err != nil {
		return nil, fmt.Errorf("decode worker environment: %v", err)
	}
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
	if isLLM {
		normalizeLLMRequestRouterAddressEnvAliases(allEnvSet)
	}

	utilsContainerImage := allEnvSet[common.UtilsImageEnv]
	if tcfg.UtilsImage != "" {
		utilsContainerImage = tcfg.UtilsImage
	}
	if utilsContainerImage == "" {
		return nil, fmt.Errorf("no utils container specified for function %s/%s of type %s",
			t.Details.FunctionID, t.Details.FunctionVersionID,
			t.Details.FunctionType)
	}
	initContainerImage := allEnvSet[common.InitImageEnv]
	if tcfg.InitImage != "" {
		initContainerImage = tcfg.InitImage
	}

	essContainerImage := allEnvSet[common.ESSAgentContainerEnv]
	hasSecretsAssertionToken := allEnvSet[common.SecretsAssertionTokenEnv] != ""
	hasTelemetries := telLaunchSpec != nil
	hasFunctionSecrets := allEnvSet[common.FunctionSecretsPresentEnv] == trueVal ||
		(!hasTelemetries && hasSecretsAssertionToken)

	instanceID := tcfg.HelmInstanceID
	if instanceID == "" {
		instanceID = tcfg.ObjectNameBase
	}

	// Volumes
	var volumes []corev1.Volume

	inferenceDir := InferenceDirPath
	inferenceVolume := corev1.Volume{
		Name: "inference-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, inferenceVolume)

	instanceDir := common.NVCFInfoDir
	instanceVolume := common.NewInstanceDataVolume()
	volumes = append(volumes, instanceVolume)

	modelsVolume := corev1.Volume{
		Name: cache.ModelDataVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, modelsVolume)

	configDir := ConfigDirPath
	configVolume := corev1.Volume{
		Name: "config-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, configVolume)

	if isLLM {
		volumes = append(volumes, newLLMClientVolumes()...)
	}

	// ESS config
	secretPath := common.FuncSecretPath(t.Details.FunctionID, t.Details.FunctionVersionID)
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

	// Helm chart download secret should be created first.
	helmChartDownloadSecret, found, err := common.ParseHelmChartDownloadSecret(launchSpec.HelmChartURL, allEnvSet)
	if err != nil {
		return nil, err
	}
	if found {
		objs = append(objs, helmChartDownloadSecret)
	}

	// Worker container creds
	var imagePullSecrets []corev1.LocalObjectReference

	if !tcfg.DisableHelmWorkloadSecretTranslation {
		workloadImagePullSecrets, err := common.ParseWorkloadImagePullSecrets(tcfg.ObjectNameBase, allEnvSet, true)
		if err != nil {
			return nil, err
		}
		for _, pullSecret := range workloadImagePullSecrets {
			// No images in generated objects need to be pulled by these.
			// They are consumed by the helm chart.
			objs = append(objs, pullSecret)
		}
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
		cacheObjs, err := cache.Translate(cls, allEnvSet, workerImagePullSecrets, t.Details.FunctionVersionID)
		if err != nil {
			return nil, fmt.Errorf("translate cache objects: %w", err)
		}
		objs = append(objs, cacheObjs...)
	}

	// Utils pod
	utilsPod := &corev1.Pod{}
	utilsPod.Name = common.UtilsPodName
	utilsPod.Spec = corev1.PodSpec{
		HostNetwork:                   tcfg.PodUseHostNetwork,
		DNSPolicy:                     tcfg.PodDNSPolicy,
		ImagePullSecrets:              imagePullSecrets,
		ServiceAccountName:            tcfg.ServiceAccountName,
		AutomountServiceAccountToken:  translateutil.NewBoolPtr(false),
		TerminationGracePeriodSeconds: new(int64),
		Volumes:                       volumes,
		Affinity:                      &corev1.Affinity{},
		RestartPolicy:                 corev1.RestartPolicyAlways,
	}
	*utilsPod.Spec.TerminationGracePeriodSeconds = 120
	common.MergeTolerations(&utilsPod.Spec, tcfg.Tolerations...)
	common.AddNVIDIAGPUNoScheduleToleration(&utilsPod.Spec)

	instanceTypeValue := t.InstanceTypeValue
	if instanceTypeValue == "" {
		instanceTypeValue = t.InstanceType //nolint:staticcheck // SA1019: deprecated field used for backward compatibility
	}
	common.SetNodeAffinity(utilsPod, tcfg.NodeAffinity, tcfg.InstanceTypeLabelSelectorKey, instanceTypeValue)
	common.SetPodAffinity(utilsPod, tcfg.PodAffinity, tcfg.PodAntiAffinity)

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
			Name:      inferenceVolume.Name,
			MountPath: inferenceDir,
		},
		{
			Name:      instanceVolume.Name,
			MountPath: instanceDir,
		},
		{
			Name:      configVolume.Name,
			MountPath: configDir,
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
	utilsEnvs = append(utilsEnvs, instanceIDEnv)

	if hasSecretsAssertionToken {
		essEnvs := []corev1.EnvVar{{
			Name:  common.EssAgentInitEnv,
			Value: "false",
		}}
		if hasFunctionSecrets {
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
		utilsPod.Spec.Containers = append(utilsPod.Spec.Containers, essContainer)

		// Add volume mounts to containers
		initContainerVolumeMounts = append(initContainerVolumeMounts,
			essDataVolumeMount)
		utilsContainerVolumeMounts = append(utilsContainerVolumeMounts,
			secretDataVolumeMountRO, essDataVolumeMount)

		// Add emptyDir ess and secret volumes
		utilsPod.Spec.Volumes = append(utilsPod.Spec.Volumes,
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
	utilsPod.Spec.InitContainers = append(utilsPod.Spec.InitContainers, initContainer)

	// The ESS init container needs to be added after the init container,
	// since the init container creates config.hcl.
	if hasSecretsAssertionToken {
		essInitEnvs := []corev1.EnvVar{{
			Name:  common.EssAgentInitEnv,
			Value: "true",
		}}
		if hasFunctionSecrets {
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
		utilsPod.Spec.InitContainers = append(utilsPod.Spec.InitContainers, essInitContainer)
	}

	if !isLLM {
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
		utilsPod.Spec.Containers = append(utilsPod.Spec.Containers, utilsContainer)
	} else {
		// llm functions don't have a utils container. they have a router client container instead.
		llmRouterClientContainer, err := newLLMRouterClientContainer(launchSpec, allEnvSet, tcfg, instanceID, true)
		if err != nil {
			return nil, err
		}
		utilsPod.Spec.Containers = append(utilsPod.Spec.Containers, llmRouterClientContainer)

		llmCredentialManagerContainer, err := newLLMCredentialManagerContainer(allEnvSet, tcfg)
		if err != nil {
			return nil, err
		}
		utilsPod.Spec.Containers = append(utilsPod.Spec.Containers, llmCredentialManagerContainer)
		applyLLMPodSecurityContext(utilsPod, tcfg)
	}

	if hasTelemetries {
		otelVersion := common.OTelVersion(allEnvSet[common.OTelContainerEnv])
		otelServiceName := common.ByooOTelCollectorPodNameBase
		objs = append(objs, common.NewOTelServiceHelm(otelServiceName))

		var otelConfigCMName string
		if otelVersion == "v1" {
			otelConfigCMName = common.ByooOTelConfigCMNameBase
			otelCM, err := common.NewOTelConfigMap(telLaunchSpec, backendconfig.TemplateConfig{
				BackendType:       backendconfig.K8s,
				WorkloadType:      backendconfig.Helm,
				Namespace:         tcfg.Namespace,
				FunctionID:        t.Details.FunctionID,
				FunctionVersionID: t.Details.FunctionVersionID,
				InstanceID:        instanceID,
				ZoneName:          tcfg.ClusterRegion,
			}, otelConfigCMName)
			if err != nil {
				return nil, err
			}
			objs = append(objs, otelCM)
		}

		if err := common.SetupPodTelemetry(
			utilsPod, telLaunchSpec, allEnvSet, common.WorkloadTypeHelm,
			tcfg.ClusterRegion, instanceID, tcfg.Namespace, common.FuncEnvVars(),
			"", otelConfigCMName, common.OTelCollectorBindAllAddresses, tcfg.OTelResources,
		); err != nil {
			return nil, err
		}
		fluentbitCM := common.SetupFluentBit(utilsPod, telLaunchSpec, allEnvSet, common.WorkloadTypeHelm, tcfg.Namespace, tcfg.FluentbitEnabled, tcfg.FluentbitResources)
		if fluentbitCM != nil {
			objs = append(objs, fluentbitCM)
		}
	}

	objs = append(objs, utilsPod)

	for _, obj := range objs {
		obj.SetNamespace(tcfg.Namespace)
		obj.SetLabels(translateutil.MergeMaps(obj.GetLabels(), commonLabels))
		obj.SetAnnotations(translateutil.MergeMaps(obj.GetAnnotations(), commonAnnotations))
	}

	return objs, nil
}
