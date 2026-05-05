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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/cache"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

func translateHelmChartUtilsDeploy(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	// Fail if LLS is enabled for utils pod as a separate pod from the inference pod
	if t.Details.FunctionType == FunctionTypeStreaming {
		return nil, fmt.Errorf("LLS is not supported for Helm functions")
	}

	if err := tcfg.ValidateHelmChart(); err != nil {
		return nil, fmt.Errorf("invalid helm chart translate config: %v", err)
	}

	launchSpec := t.LaunchSpecification

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
	allEnvSet[common.GPUCountEnv] = strconv.Itoa(int(t.RequestedGPUCount)) //nolint:gosec // G115: intentional uint64->int conversion, values are bounded

	utilsContainerImage := allEnvSet[common.UtilsImageEnv]
	if tcfg.UtilsImage != "" {
		utilsContainerImage = tcfg.UtilsImage
	}
	initContainerImage := allEnvSet[common.InitImageEnv]
	if tcfg.InitImage != "" {
		initContainerImage = tcfg.InitImage
	}

	essContainerImage := allEnvSet[common.ESSAgentContainerEnv]
	needsESSAgentContainer := allEnvSet[common.SecretsAssertionTokenEnv] != ""

	instanceID := tcfg.ObjectNameBase

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

	// ESS config
	const essAgentInitEnvName = "ESS_AGENT_INIT"
	secretPathEnv := corev1.EnvVar{
		Name:  "SECRET_PATH",
		Value: fmt.Sprintf("functions/%s/versions/%s", t.Details.FunctionID, t.Details.FunctionVersionID),
	}
	essDataVolumeMount := corev1.VolumeMount{
		Name:      "ess-data",
		MountPath: "/config/ess-agent",
	}
	secretDataVolumeMount := corev1.VolumeMount{
		Name:      "secret-data",
		MountPath: "/var/secrets",
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

	// Pull secrets
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
	namespaceEnv := corev1.EnvVar{
		Name:  "HELM_CHART_NAMESPACE",
		Value: tcfg.ObjectNameBase,
	}
	initEnvs := common.MapToEnv(allEnvSet)
	initEnvs = append(initEnvs, instanceIDEnv)
	utilsEnvs := common.MapToEnv(allEnvSet)
	utilsEnvs = append(utilsEnvs, instanceIDEnv)
	utilsEnvs = append(utilsEnvs, namespaceEnv)

	if needsESSAgentContainer {
		essEnvs := []corev1.EnvVar{
			secretPathEnv,
			{
				Name:  essAgentInitEnvName,
				Value: "false",
			},
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
	if needsESSAgentContainer {
		essInitEnvs := []corev1.EnvVar{
			secretPathEnv,
			{
				Name:  essAgentInitEnvName,
				Value: "true",
			},
		}

		essInitContainer := common.NewESSInitContainer(essContainerImage, essInitEnvs)
		essInitContainer.VolumeMounts = []corev1.VolumeMount{
			secretDataVolumeMount,
			essDataVolumeMount,
		}
		utilsPod.Spec.InitContainers = append(utilsPod.Spec.InitContainers, essInitContainer)
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
	utilsPod.Spec.Containers = append(utilsPod.Spec.Containers, utilsContainer)

	replicas := int32(1)
	utilsDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-utils", tcfg.ObjectNameBase),
			Namespace: utilsPod.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: translateutil.MergeMaps(utilsPod.Labels, commonLabels),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: translateutil.MergeMaps(utilsPod.Labels, commonLabels),
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  translateutil.NewBoolPtr(false),
					TerminationGracePeriodSeconds: new(int64),
					Affinity:                      &corev1.Affinity{},
					HostNetwork:                   utilsPod.Spec.HostNetwork,
					ServiceAccountName:            utilsPod.Spec.ServiceAccountName,
					ImagePullSecrets:              utilsPod.Spec.ImagePullSecrets,
					Containers:                    []corev1.Container{utilsContainer},
					Volumes:                       utilsPod.Spec.Volumes,
				},
			},
		},
	}
	*utilsDeployment.Spec.Template.Spec.TerminationGracePeriodSeconds = 120
	utilsPod.Spec.Affinity.DeepCopyInto(utilsDeployment.Spec.Template.Spec.Affinity)
	utilsDeployment.Spec.Template.Spec.Tolerations = append(
		utilsDeployment.Spec.Template.Spec.Tolerations,
		utilsPod.Spec.Tolerations...,
	)

	objs = append(objs, utilsDeployment)

	for _, obj := range objs {
		obj.SetNamespace(tcfg.Namespace)
		obj.SetLabels(translateutil.MergeMaps(obj.GetLabels(), commonLabels))
		obj.SetAnnotations(translateutil.MergeMaps(obj.GetAnnotations(), commonAnnotations))
	}

	return objs, nil
}
