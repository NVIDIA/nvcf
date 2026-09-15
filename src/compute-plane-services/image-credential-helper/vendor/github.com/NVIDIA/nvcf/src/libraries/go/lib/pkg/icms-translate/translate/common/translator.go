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

package common

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	utilerror "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"

	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
)

const (
	UtilsHealthPort int32 = 8080

	UtilsContainerName = "utils"
	UtilsPodName       = "utils"
	InitContainerName  = "init"
	ESSContainerName   = "ess"

	TermLogPath = "/dev/termination-log"
	DevShmPath  = "/dev/shm"
	NVCFInfoDir = "/var/run/nvcf/info"

	NVIDIAGPUTolerationKey = "nvidia.com/gpu"

	HelmWorkloadPullSecretName = "inference-container-pull-secret"
)

type BackendType string

type TranslateConfig struct {
	Namespace          string            `json:"namespace,omitempty"`
	CommonLabels       map[string]string `json:"commonLabels,omitempty"`
	CommonAnnotations  map[string]string `json:"commonAnnotations,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`

	InitImage  string `json:"initImage,omitempty"`
	UtilsImage string `json:"utilsImage,omitempty"`

	NodeAffinity                 *corev1.NodeAffinity    `json:"nodeAffinity,omitempty"`
	PodAffinity                  *corev1.PodAffinity     `json:"podAffinity,omitempty"`
	PodAntiAffinity              *corev1.PodAntiAffinity `json:"podAntiAffinity,omitempty"`
	Tolerations                  []corev1.Toleration     `json:"tolerations,omitempty"`
	InstanceTypeLabelSelectorKey string                  `json:"instanceTypeLabelSelectorKey,omitempty"`

	WorkloadResources  corev1.ResourceRequirements `json:"workloadResources,omitempty"`
	OTelResources      corev1.ResourceRequirements `json:"otelResources,omitempty"`
	FluentbitResources corev1.ResourceRequirements `json:"fluentbitResources,omitempty"`
	FluentbitEnabled   bool                        `json:"fluentbitEnabled,omitempty"`

	ObjectNameBase string `json:"objectNameBase,omitempty"`
	HelmInstanceID string `json:"helmInstanceID,omitempty"`

	OTelBackendType BackendType `json:"otelBackendType"`

	ClusterRegion string `json:"clusterRegion"`
	ClusterName   string `json:"clusterName"`
}

func (c *TranslateConfig) Default() {
	if c.ObjectNameBase == "" {
		c.ObjectNameBase = uuid.NewString()
	}
	if c.OTelResources.Requests == nil && c.OTelResources.Limits == nil {
		c.OTelResources = GetDefaultContainerResourcesBYOO()
	}
	if c.FluentbitResources.Requests == nil && c.FluentbitResources.Limits == nil {
		c.FluentbitResources = GetDefaultContainerResourcesFluentbit()
	}
}

func (c TranslateConfig) ValidateContainer() (err error) {
	var (
		unsetFields []string
		errs        []error
	)

	vfields, verrs := c.validateBase()
	errs = append(errs, verrs...)
	unsetFields = append(unsetFields, vfields...)

	vfields, verrs = c.validateResources()
	errs = append(errs, verrs...)
	unsetFields = append(unsetFields, vfields...)

	if len(unsetFields) != 0 {
		errs = append(errs, fmt.Errorf("required fields: %+q", unsetFields))
	}

	return utilerror.NewAggregate(errs)
}

func (c TranslateConfig) ValidateHelmChart() (err error) {
	var (
		unsetFields []string
		errs        []error
	)

	vfields, verrs := c.validateBase()
	errs = append(errs, verrs...)
	unsetFields = append(unsetFields, vfields...)

	if len(unsetFields) != 0 {
		errs = append(errs, fmt.Errorf("required fields: %+q", unsetFields))
	}

	return utilerror.NewAggregate(errs)
}

func (c TranslateConfig) validateResources() (unsetFields []string, errs []error) {
	if len(c.WorkloadResources.Requests) == 0 && len(c.WorkloadResources.Limits) == 0 {
		unsetFields = append(unsetFields, "workloadResources.requests")
	} else {
		for _, rl := range []corev1.ResourceList{
			c.WorkloadResources.Requests,
			c.WorkloadResources.Limits,
		} {
			for lk, lv := range rl {
				if strings.HasPrefix(lk.String(), "nvidia.com/") && !lv.IsZero() {
					goto done
				}
			}
		}
		errs = append(errs, fmt.Errorf("no GPU resource request or limit found"))
	}
done:

	return unsetFields, errs
}

func (c TranslateConfig) validateBase() (unsetFields []string, errs []error) {
	if c.InstanceTypeLabelSelectorKey == "" {
		unsetFields = append(unsetFields, "instanceTypeLabelSelectorKey")
	}

	return unsetFields, errs
}

type MetadataOptions struct {
	FunctionID        string
	FunctionVersionID string
	FunctionName      string
	TaskID            string
	TaskName          string
}

func NewCommonMetadata(
	t CreationQueueMessageMetadata,
	tcfg TranslateConfig,
	icmsEnv string,
	opts MetadataOptions,
) (labels, annotations map[string]string) {
	ncaIDLabelVal := "nca-" + t.NCAID + "-nca"
	newLabels := map[string]string{
		"icms-request-id":                 t.RequestID,
		"nca-id":                          ncaIDLabelVal,
		"nvcf.nvidia.io/message-batch-id": t.MessageBatchID,
		"gpu-name":                        t.GPUType,
		"environment":                     icmsEnv,
		"performance_class":               t.InstanceTypeValue,
		// Upper-case labels are for metrics and logging.
		// NB: These should be migrated to lower-case labels in the future.
		"ENVIRONMENT":      icmsEnv,
		"GPU_COUNT":        fmt.Sprint(t.RequestedGPUCount),
		NCAIDEncodedEnvKey: ncaIDLabelVal,
	}
	if opts.FunctionID != "" {
		newLabels["function-id"] = opts.FunctionID
		newLabels["FUNCTION_ID"] = opts.FunctionID
	}
	if opts.FunctionVersionID != "" {
		newLabels["function-version-id"] = opts.FunctionVersionID
		newLabels["FUNCTION_VERSION_ID"] = opts.FunctionVersionID
	}
	if opts.TaskID != "" {
		newLabels["task-id"] = opts.TaskID
		newLabels["TASK_ID"] = opts.TaskID
	}
	if t.DeploymentID != "" {
		newLabels["nvcf.nvidia.io/deployment-id"] = t.DeploymentID
	}
	if t.GPUSpecificationID != "" {
		newLabels["nvcf.nvidia.io/gpu-specification-id"] = t.GPUSpecificationID
	}
	labels = translateutil.MergeMaps(tcfg.CommonLabels, newLabels)
	annotations = translateutil.MergeMaps(tcfg.CommonAnnotations, map[string]string{
		"nca-id":                             t.NCAID,
		"instance-count":                     fmt.Sprint(t.InstanceCount),
		"nvcf.nvidia.io/region":              tcfg.ClusterRegion,
		"nvcf.nvidia.io/backend":             tcfg.ClusterName,
		"nvcf.nvidia.io/instance-type-name":  t.InstanceTypeName,
		"nvcf.nvidia.io/instance-type-value": t.InstanceTypeValue,
		"nvcf.nvidia.io/environment":         icmsEnv,
	})
	if opts.FunctionName != "" {
		annotations["function-name"] = opts.FunctionName
		annotations[FunctionNameEncodedEnvKey] = opts.FunctionName
	}
	if opts.TaskName != "" {
		annotations["task-name"] = opts.TaskName
		annotations[TaskNameEncodedEnvKey] = opts.TaskName
	}
	return labels, annotations
}

func NewWorkloadContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"NET_BIND_SERVICE"},
		},
	}
}

func NewInfraContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"NET_RAW"},
		},
	}
}

func NewDevShmVolume() corev1.Volume {
	return corev1.Volume{
		Name: "dshm",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium: corev1.StorageMediumMemory,
			},
		},
	}
}

func NewInstanceDataVolume() corev1.Volume {
	return corev1.Volume{
		Name: "instance-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}

func SetNodeAffinity(pod *corev1.Pod, newNA *corev1.NodeAffinity, label, value string) {
	if newNA != nil {
		pod.Spec.Affinity.NodeAffinity = newNA.DeepCopy()
	} else {
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}

	if pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	if len(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      label,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{value},
			}},
		}}
	} else {
		for i := range pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions = append(
				pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions,
				corev1.NodeSelectorRequirement{
					Key:      label,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{value},
				},
			)
		}
	}
}

func SetPodAffinity(pod *corev1.Pod, newPA *corev1.PodAffinity, newPAA *corev1.PodAntiAffinity) {
	if newPA != nil && !reflect.ValueOf(newPA).IsZero() {
		pod.Spec.Affinity.PodAffinity = newPA.DeepCopy()
	}
	if newPAA != nil && !reflect.ValueOf(newPAA).IsZero() {
		pod.Spec.Affinity.PodAntiAffinity = newPAA.DeepCopy()
	}
}

// MergeTolerations appends tolerations to the pod spec when an exact match is not already present.
func MergeTolerations(podSpec *corev1.PodSpec, tolerations ...corev1.Toleration) (added bool) {
	for _, toleration := range tolerations {
		found := false
		for _, existing := range podSpec.Tolerations {
			if existing == toleration {
				found = true
				break
			}
		}
		if found {
			continue
		}
		podSpec.Tolerations = append(podSpec.Tolerations, toleration)
		added = true
	}
	return added
}

// AddNVIDIAGPUNoScheduleToleration adds a toleration for nvidia.com/gpu to the pod. Returns true if a toleration was added, false otherwise.
func AddNVIDIAGPUNoScheduleToleration(podSpec *corev1.PodSpec) (added bool) {
	return MergeTolerations(podSpec, corev1.Toleration{
		Key:      NVIDIAGPUTolerationKey,
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	})
}

func MutateUtilsProbes(containerSpec *corev1.Container) {
	const (
		liveEndpoint  = "/v1/health/live"
		readyEndpoint = "/v1/health/ready"
	)
	containerSpec.StartupProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: liveEndpoint,
				Port: intstr.FromInt32(UtilsHealthPort),
			},
		},
		InitialDelaySeconds: 0,
		PeriodSeconds:       1,
		FailureThreshold:    60,
		SuccessThreshold:    1,
		TimeoutSeconds:      1,
	}

	containerSpec.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: readyEndpoint,
				Port: intstr.FromInt32(UtilsHealthPort),
			},
		},
		InitialDelaySeconds: 0,
		PeriodSeconds:       10,
		FailureThreshold:    2,
		SuccessThreshold:    1,
		TimeoutSeconds:      6,
	}

	containerSpec.LivenessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: liveEndpoint,
				Port: intstr.FromInt32(UtilsHealthPort),
			},
		},
		InitialDelaySeconds: 0,
		PeriodSeconds:       10,
		FailureThreshold:    2,
		SuccessThreshold:    1,
		TimeoutSeconds:      1,
	}
}
