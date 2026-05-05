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

package mscontroller

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	translateutil "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/util"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type objectMutator interface {
	mutate(context.Context, client.Object) error
}

type objectMutatorFunc func(context.Context, client.Object) error

func (f objectMutatorFunc) mutate(ctx context.Context, obj client.Object) error { return f(ctx, obj) }

type objectMutatorSet map[schema.GroupVersionKind][]objectMutator

func newEmptyObjectMutators() objectMutatorSet {
	oms := objectMutatorSet{}
	for gvk := range knownGKVs {
		oms[gvk] = []objectMutator{}
	}
	return oms
}

func newGenericMutator(
	ff featureflag.Fetcher,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	clusterRegion string,
	clusterName string,
	functionName string,
	taskName string,
	isInfra bool,
) objectMutator {
	labels, annotations := newGeneralObjectLabelsAndAnnotations(ff, ms, icmsReq,
		clusterRegion, clusterName,
		functionName, taskName,
		isInfra,
	)
	return objectMutatorFunc(func(_ context.Context, o client.Object) error {
		o.SetLabels(mergeMaps(o.GetLabels(), labels))
		o.SetAnnotations(mergeMaps(o.GetAnnotations(), annotations))
		return nil
	})
}

func (objMutators objectMutatorSet) setGeneralObjectMutatorsForRequest(
	fff featureflag.Fetcher,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	clusterRegion string,
	clusterName string,
	functionName string,
	taskName string,
	isInfra bool,
) {
	objMutators.setGeneralObjectMetadataMutators(fff, ms, icmsReq, clusterRegion, clusterName, functionName, taskName, isInfra)

	if fff.IsFeatureFlagEnabled(featureflag.HelmAllowCPUNodes) {
		objMutators.setPerPodInstanceTypeNodeAffinityMutators(
			nodefeatures.UniformInstanceTypeLabelKey,
			icmsReq.Spec.CreationMsgInfo.GetInstanceTypeLabelSelValue(),
		)
	} else {
		objMutators.setInstanceTypeNodeAffinityMutators(
			nodefeatures.UniformInstanceTypeLabelKey,
			icmsReq.Spec.CreationMsgInfo.GetInstanceTypeLabelSelValue(),
		)
	}
}

func newInstanceIDEnv(workloadType common.MessageAction) corev1.EnvVar {
	var envName string
	if workloadType == common.TaskCreationAction {
		envName = nvcatypes.NVCTInstIDEnvKey
	} else {
		envName = nvcatypes.NVCFInstIDEnvKey
	}
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				FieldPath: fmt.Sprintf("metadata.labels['%s']", miniserviceNameLabel),
			},
		},
	}
}

func newWorkloadTelemetriesEnvVars(
	byooLaunchSpec *common.TelemetriesLaunchSpecification,
	byooSvc *corev1.Service,
) ([]corev1.EnvVar, error) {
	if byooSvc == nil {
		return nil, reconcile.TerminalError(fmt.Errorf("byoo service not found"))
	}
	envs := common.MakeOTelEnvSet(byooLaunchSpec, byooSvc.Name)
	return envs, nil
}

const (
	// This label must be set on all created objects so they are reconciled by the controller.
	miniserviceNameLabel = nvcatypes.MiniserviceNameLabel
)

func newGeneralObjectLabelsAndAnnotations(
	ff featureflag.Fetcher,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	clusterRegion string,
	clusterName string,
	functionName string,
	taskName string,
	isInfra bool,
) (labels, annotations map[string]string) {
	labels, annotations = nvcatypes.GetLabelsForRequest(icmsReq, ff), nvcatypes.GetAnnotationsForRequest(icmsReq)

	labels[miniserviceNameLabel] = ms.Name

	if isInfra {
		annotations[nvcatypes.InfraObjectAnnotationKey] = "true"
	}

	stlLabels, stlAnnotations := common.NewCommonMetadata(
		icmsReq.Spec.CreationMsgInfo.CreationQueueMessageMetadata,
		common.TranslateConfig{
			Namespace:                    ms.Spec.Namespace,
			ObjectNameBase:               icmsReq.Name,
			InstanceTypeLabelSelectorKey: nodefeatures.UniformInstanceTypeLabelKey,
			WorkloadResources:            corev1.ResourceRequirements{},
			ClusterRegion:                clusterRegion,
			ClusterName:                  clusterName,
		},
		icmsReq.Spec.GetICMSEnvironment(),
		common.MetadataOptions{
			// TODO(mcamp): add FunctionName
			FunctionID:        icmsReq.Spec.FunctionDetails.FunctionID,
			FunctionVersionID: icmsReq.Spec.FunctionDetails.FunctionVersionID,
			FunctionName:      functionName,
			TaskID:            icmsReq.Spec.TaskDetails.TaskID,
			TaskName:          taskName,
		},
	)
	labels = mergeMaps(labels, stlLabels)
	annotations = mergeMaps(annotations, stlAnnotations)
	return labels, annotations
}

func (objMutators objectMutatorSet) setGeneralObjectMetadataMutators(
	ff featureflag.Fetcher,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	clusterRegion string,
	clusterName string,
	functionName string,
	taskName string,
	isInfra bool,
) {
	labels, annotations := newGeneralObjectLabelsAndAnnotations(ff, ms, icmsReq,
		clusterRegion, clusterName,
		functionName, taskName,
		isInfra,
	)

	for key := range objMutators {
		// Set annotations on the object itself.
		objMutators[key] = append(objMutators[key], objectMutatorFunc(func(_ context.Context, o client.Object) error {
			o.SetLabels(mergeMaps(o.GetLabels(), labels))
			o.SetAnnotations(mergeMaps(o.GetAnnotations(), annotations))
			return nil
		}))
		// Then set annotations on pod spec/generator objects.
		var mf objectMutatorFunc
		switch key {
		case deploymentGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.Deployment)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, labels)
				ot.Spec.Template.Annotations = mergeMaps(ot.Spec.Template.Annotations, annotations)
				return nil
			})
		case replicaSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.ReplicaSet)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, labels)
				ot.Spec.Template.Annotations = mergeMaps(ot.Spec.Template.Annotations, annotations)
				return nil
			})
		case statefulSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.StatefulSet)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, labels)
				ot.Spec.Template.Annotations = mergeMaps(ot.Spec.Template.Annotations, annotations)
				for i, pvc := range ot.Spec.VolumeClaimTemplates {
					ot.Spec.VolumeClaimTemplates[i].Labels = mergeMaps(pvc.Labels, labels)
					ot.Spec.VolumeClaimTemplates[i].Annotations = mergeMaps(pvc.Annotations, annotations)
				}
				return nil
			})
		case jobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.Job)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, labels)
				ot.Spec.Template.Annotations = mergeMaps(ot.Spec.Template.Annotations, annotations)
				return nil
			})
		case cronJobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.CronJob)
				ot.Spec.JobTemplate.Labels = mergeMaps(ot.Spec.JobTemplate.Labels, labels)
				ot.Spec.JobTemplate.Annotations = mergeMaps(ot.Spec.JobTemplate.Annotations, annotations)
				ot.Spec.JobTemplate.Spec.Template.Labels = mergeMaps(
					ot.Spec.JobTemplate.Spec.Template.Labels, labels)
				ot.Spec.JobTemplate.Spec.Template.Annotations = mergeMaps(
					ot.Spec.JobTemplate.Spec.Template.Annotations, annotations)
				return nil
			})
		default:
			continue
		}
		objMutators[key] = append(objMutators[key], mf)
	}
}

func (objMutators objectMutatorSet) setInstanceTypeNodeAffinityMutators(ikey, ival string) {
	objMutators.mutateAllPodSpecTypes(func(ps *corev1.PodSpec) {
		setInstanceTypeNodeAffinity(ps, ikey, ival)
	})
}

func (objMutators objectMutatorSet) mutateAllPodSpecTypes(psMF func(*corev1.PodSpec)) {
	for key := range objMutators {
		var mf objectMutatorFunc
		switch key {
		case podGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*corev1.Pod)
				psMF(&ot.Spec)
				return nil
			})
		case deploymentGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.Deployment)
				psMF(&ot.Spec.Template.Spec)
				return nil
			})
		case replicaSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.ReplicaSet)
				psMF(&ot.Spec.Template.Spec)
				return nil
			})
		case statefulSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.StatefulSet)
				psMF(&ot.Spec.Template.Spec)
				return nil
			})
		case jobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.Job)
				psMF(&ot.Spec.Template.Spec)
				return nil
			})
		case cronJobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.CronJob)
				psMF(&ot.Spec.JobTemplate.Spec.Template.Spec)
				return nil
			})
		default:
			continue
		}
		objMutators[key] = append(objMutators[key], mf)
	}
}

func setInstanceTypeNodeAffinity(pts *corev1.PodSpec, ikey, ival string) {
	k8sutil.SetInstanceTypeNodeAffinity(pts, ikey, ival)
}

func (objMutators objectMutatorSet) setPerPodInstanceTypeNodeAffinityMutators(ikey, ival string) {
	objMutators.mutateAllPodSpecTypes(func(ps *corev1.PodSpec) {
		if k8sutil.PodSpecRequestsGPU(ps) {
			setInstanceTypeNodeAffinity(ps, ikey, ival)
		} else {
			setCPUWorkloadNodeAffinity(ps)
		}
	})
}

func setCPUWorkloadNodeAffinity(pts *corev1.PodSpec) {
	k8sutil.SetCPUWorkloadNodeAffinity(pts, nodefeatures.UniformInstanceTypeLabelKey)
}

// setKAISchedulerMutators sets the schedulerName to "kai-scheduler" on all workload PodSpecs
// and adds the kai.scheduler/queue label to pods & podSpec for all workload types.
func (objMutators objectMutatorSet) setKAISchedulerMutators() {
	objMutators.mutateAllPodSpecTypes(func(ps *corev1.PodSpec) {
		ps.SchedulerName = kaischeduler.SchedulerName
	})

	queueLabel := map[string]string{
		kaischeduler.SchedulerQueueLabel: kaischeduler.GetQName(),
	}
	for key := range objMutators {
		var mf objectMutatorFunc
		switch key {
		case podGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				o.SetLabels(mergeMaps(o.GetLabels(), queueLabel))
				return nil
			})
		case deploymentGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.Deployment)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, queueLabel)
				return nil
			})
		case replicaSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.ReplicaSet)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, queueLabel)
				return nil
			})
		case statefulSetGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*appsv1.StatefulSet)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, queueLabel)
				return nil
			})
		case jobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.Job)
				ot.Spec.Template.Labels = mergeMaps(ot.Spec.Template.Labels, queueLabel)
				return nil
			})
		case cronJobGVK:
			mf = objectMutatorFunc(func(_ context.Context, o client.Object) error {
				ot := o.(*batchv1.CronJob)
				ot.Spec.JobTemplate.Spec.Template.Labels = mergeMaps(
					ot.Spec.JobTemplate.Spec.Template.Labels, queueLabel)
				return nil
			})
		default:
			continue
		}
		objMutators[key] = append(objMutators[key], mf)
	}
}

// setUtilsHelmTaskPollProgressEnv configures a helm task utils pod
// to poll the progress file. This is a necessary setting when the task progress dir's filesystem
// is backed by SMB, which does not support util's file watcher.
func setUtilsHelmTaskPollProgressEnv(log logr.Logger, utilsPod *corev1.Pod, maxRuntimeDurationStr string) {
	envs := []corev1.EnvVar{
		{
			Name:  "POLL_PROGRESS",
			Value: "true",
		},
	}

	// The utils default poll period for task progress files is 5 minutes.
	// If the max runtime duration is shorter, the task may exit before progress is polled.
	// To avoid this case, use half of the max runtime duration if under the default.
	if maxRuntimeDurationStr != "" {
		if mrt, err := translateutil.ParseISO8601Duration(maxRuntimeDurationStr); err != nil {
			log.Error(err, "Error parsing max runtime duration for helm task, using utils default poll period")
		} else if mrt <= 5*time.Minute {
			envs = append(envs, corev1.EnvVar{
				Name:  "POLL_PROGRESS_INTERVAL",
				Value: (mrt / 2).String(),
			})
		}
	}

	for i, ci := range utilsPod.Spec.Containers {
		if ci.Name == common.UtilsContainerName {
			k8sutil.AddEnvsToContainer(&utilsPod.Spec.Containers[i], envs...)
			break
		}
	}
}

// setImagePullSecretMutators updates all pod spec image pull secrets with the names of secrets.
// This is needed even with service account pull secrets because those two lists are *not* merged.
//
// See https://github.com/kubernetes/kubernetes/blob/6f093ef/plugin/pkg/admission/serviceaccount/admission.go#L167-L171
func (objMutators objectMutatorSet) setImagePullSecretMutators(secrets []*corev1.Secret) {
	secretNameSet := sets.New[string]()
	secretRefs := make([]corev1.LocalObjectReference, 0, len(secrets))
	for _, secret := range secrets {
		secretNameSet.Insert(secret.Name)
		secretRefs = append(secretRefs, corev1.LocalObjectReference{Name: secret.Name})
	}
	updateImagePullSecrets := func(imagePullSecrets []corev1.LocalObjectReference) (out []corev1.LocalObjectReference) {
		for _, secret := range imagePullSecrets {
			if !secretNameSet.Has(secret.Name) {
				out = append(out, secret)
			}
		}
		out = append(out, secretRefs...)

		sort.Slice(out, func(i, j int) bool {
			return out[i].Name < out[j].Name
		})

		return out
	}
	objMutators.mutateAllPodSpecTypes(func(ps *corev1.PodSpec) {
		ps.ImagePullSecrets = updateImagePullSecrets(ps.ImagePullSecrets)
	})
}

func mergeMaps(m1, m2 map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range m1 {
		merged[k] = v
	}
	for k, v := range m2 {
		merged[k] = v
	}
	return merged
}
