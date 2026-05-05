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

	"github.com/google/shlex"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/cache"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

/*
Container Functions will be translated as the following set of artifacts
  - an inference Pod
  - a utils deployment
  - a service endpoint for inference
*/
func translateContainerUtilsDeploy(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	if err := tcfg.ValidateContainer(); err != nil {
		return nil, fmt.Errorf("invalid container translate config: %v", err)
	}

	launchSpec := t.LaunchSpecification

	allEnvSet, err := common.DecodeEnvironmentB64(launchSpec.EnvironmentB64, common.EnvDecoderText)
	if err != nil {
		return nil, fmt.Errorf("decode worker environment: %v", err)
	}

	commonLabels, commonAnnotations := common.NewCommonMetadata(
		t.CreationQueueMessageMetadata, tcfg.TranslateConfig,
		launchSpec.ICMSEnvironment, common.MetadataOptions{
			FunctionID:        t.Details.FunctionID,
			FunctionVersionID: t.Details.FunctionVersionID,
			FunctionName:      allEnvSet[common.FunctionNameEncodedEnvKey],
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
	allEnvSet[common.GPUCountEnv] = strconv.Itoa(int(t.RequestedGPUCount)) //nolint:gosec // G115: intentional uint64->int conversion, values are bounded

	inferenceContainerImage := allEnvSet[common.ContainerFunctionImageEnv]
	if inferenceContainerImage == "" {
		return nil, fmt.Errorf("no inference container specified")
	}
	initContainerImage := allEnvSet[common.InitImageEnv]
	if tcfg.InitImage != "" {
		initContainerImage = tcfg.InitImage
	}

	instanceIDBase := tcfg.ObjectNameBase
	targetPort := allEnvSet["INFERENCE_PORT"]

	// Volumes
	var volumes []corev1.Volume

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

	dshmVolume := common.NewDevShmVolume()
	volumes = append(volumes, dshmVolume)

	// Pull secrets.
	var imagePullSecrets []corev1.LocalObjectReference

	workloadImagePullSecrets, err := common.ParseWorkloadImagePullSecrets(tcfg.ObjectNameBase, allEnvSet, false)
	if err != nil {
		return nil, err
	}
	for _, pullSecret := range workloadImagePullSecrets {
		objs = append(objs, pullSecret)
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: pullSecret.Name})
	}

	// Parse inference args, if any.
	var inferenceArgs []string
	if argsStr := allEnvSet[common.InferenceContainerArgsEnv]; argsStr != "" {
		args, err := shlex.Split(argsStr)
		if err != nil {
			return nil, fmt.Errorf("parse container args: %v", err)
		}
		inferenceArgs = append(inferenceArgs, args...)
	}

	// Inference envs with extra helper envs.
	inferenceEnvSet := map[string]string{}
	if envB64 := allEnvSet[common.InferenceContainerEnvEnv]; envB64 != "" {
		inferenceEnvSet, err = common.DecodeEnvironmentB64(envB64, common.EnvDecoderJSON)
		if err != nil {
			return nil, fmt.Errorf("decode task environment: %v", err)
		}
	}
	inferenceEnvs, _ := common.MergeEnvs(common.MapToEnv(inferenceEnvSet), common.MakeWorkloadEnvVars(common.FunctionCreationAction)...)
	inferenceEnvs = common.SortEnvs(inferenceEnvs)

	// Pod instances
	instanceCount := int(t.InstanceCount) //nolint:gosec // G115: intentional uint64->int conversion, values are bounded
	if instanceCount == 0 {
		instanceCount = 1
	}
	for i := 0; i < instanceCount; i++ {
		instanceID := fmt.Sprintf("%d-%s-inference", i, instanceIDBase)

		inferencePod := &corev1.Pod{}
		inferencePod.Name = instanceID
		inferencePod.Spec = corev1.PodSpec{
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
		*inferencePod.Spec.TerminationGracePeriodSeconds = 120
		common.MergeTolerations(&inferencePod.Spec, tcfg.Tolerations...)
		common.AddNVIDIAGPUNoScheduleToleration(&inferencePod.Spec)

		instanceTypeValue := t.InstanceTypeValue
		if instanceTypeValue == "" {
			instanceTypeValue = t.InstanceType //nolint:staticcheck // SA1019: deprecated field used for backward compatibility
		}
		common.SetNodeAffinity(inferencePod, tcfg.NodeAffinity, tcfg.InstanceTypeLabelSelectorKey, instanceTypeValue)
		common.SetPodAffinity(inferencePod, tcfg.PodAffinity, tcfg.PodAntiAffinity)

		inferenceContainerVolumeMounts := append(cache.NewModelDataVolumeMounts(),
			corev1.VolumeMount{
				Name:      inferenceVolume.Name,
				MountPath: inferenceDir,
			},
			corev1.VolumeMount{
				Name:      instanceVolume.Name,
				MountPath: instanceDir,
				ReadOnly:  true,
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
		)

		// Infra environment
		instanceIDEnv := corev1.EnvVar{
			Name:  "INSTANCE_ID",
			Value: instanceID,
		}
		initEnvs := common.MapToEnv(allEnvSet)
		initEnvs = append(initEnvs, instanceIDEnv)

		inferenceContainer := corev1.Container{
			Name:            inferenceContainerName,
			Image:           inferenceContainerImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Args:            inferenceArgs,
			Env:             inferenceEnvs,
			Resources:       tcfg.WorkloadResources,
			SecurityContext: common.NewWorkloadContainerSecurityContext(),
			VolumeMounts:    inferenceContainerVolumeMounts,
		}
		inferencePod.Spec.Containers = append(inferencePod.Spec.Containers, inferenceContainer)

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
		inferencePod.Spec.InitContainers = append(inferencePod.Spec.InitContainers, initContainer)

		objs = append(objs, inferencePod)
	}

	utilsArtifacts, err := getUtilsDeploymentAndSecrets(t, tcfg)
	if err != nil {
		return nil, fmt.Errorf("make utils deployment and artifacts: %v", err)
	}

	objs = append(objs, utilsArtifacts...)

	serviceBindingLabels := map[string]string{
		"app.kubernetes.io/instance": "inference",
	}

	infSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inference",
			Namespace: tcfg.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: serviceBindingLabels,
			Type:     corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(targetPort),
					Port:       8000,
				},
			},
		},
	}
	objs = append(objs, infSvc)

	for _, obj := range objs {
		obj.SetNamespace(tcfg.Namespace)
		obj.SetLabels(translateutil.MergeMaps(obj.GetLabels(), commonLabels))
		obj.SetLabels(translateutil.MergeMaps(obj.GetLabels(), serviceBindingLabels))
		obj.SetAnnotations(translateutil.MergeMaps(obj.GetAnnotations(), commonAnnotations))
	}

	return objs, nil
}

func getUtilsDeploymentAndSecrets(t CreationQueueMessage, tcfg TranslateConfig) (objs []metav1.Object, err error) {
	// Fail if LLS is enabled for utils pod as a separate pod from the inference pod
	if t.Details.FunctionType == FunctionTypeStreaming {
		return nil, fmt.Errorf("LLS is not supported for utils deployments")
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

	// Pull secrets
	var imagePullSecrets []corev1.LocalObjectReference

	workerImagePullSecrets, err := common.ParseWorkerImagePullSecrets(tcfg.ObjectNameBase, allEnvSet)
	if err != nil {
		return nil, err
	}
	for _, pullSecret := range workerImagePullSecrets {
		objs = append(objs, pullSecret)
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: pullSecret.Name})
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

	serviceNamespaceEnvs := []corev1.EnvVar{
		{
			Name:  "HELM_CHART_NAMESPACE",
			Value: tcfg.ObjectNameBase,
		},
		{
			Name:  "HELM_CHART_INFERENCE_SERVICE_NAME",
			Value: "inference-service",
		},
	}
	initEnvs := common.MapToEnv(allEnvSet)
	initEnvs = append(initEnvs, instanceIDEnv)
	utilsEnvs := common.MapToEnv(allEnvSet)
	utilsEnvs = append(utilsEnvs, instanceIDEnv)
	utilsEnvs = append(utilsEnvs, serviceNamespaceEnvs...)

	// TODO: Add LLS environment variables if any are provided
	// once we add LLS for deployments

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
				MatchLabels: commonLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      commonLabels,
					Annotations: commonAnnotations,
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  translateutil.NewBoolPtr(false),
					TerminationGracePeriodSeconds: new(int64),
					Affinity:                      &corev1.Affinity{},
					RestartPolicy:                 corev1.RestartPolicyAlways,
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

	return objs, nil
}
