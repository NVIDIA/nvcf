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
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential"
	"github.com/imdario/mergo"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	defaultServiceAccountName = "default"
	// serviceAccountName for all workload objects
	serviceAccountName = "miniservice-instance-permissions"

	instanceRBACConfigMapName = "nvca-miniservice-rbac"
	// This Role will be created externally in the requests namespace.
	// They must be copied to each new namespace for the mini service instance.
	instanceRoleName = "mini-service-restrictions"
)

var (
	podGVK            = corev1.SchemeGroupVersion.WithKind("Pod")
	secretGVK         = corev1.SchemeGroupVersion.WithKind("Secret")
	serviceGVK        = corev1.SchemeGroupVersion.WithKind("Service")
	configMapGVK      = corev1.SchemeGroupVersion.WithKind("ConfigMap")
	pvcGVK            = corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim")
	deploymentGVK     = appsv1.SchemeGroupVersion.WithKind("Deployment")
	replicaSetGVK     = appsv1.SchemeGroupVersion.WithKind("ReplicaSet")
	statefulSetGVK    = appsv1.SchemeGroupVersion.WithKind("StatefulSet")
	jobGVK            = batchv1.SchemeGroupVersion.WithKind("Job")
	cronJobGVK        = batchv1.SchemeGroupVersion.WithKind("CronJob")
	storageRequestGVK = nvcav2beta1.SchemeGroupVersion.WithKind("StorageRequest")

	// All GVK's that can be reconciled by this controller.
	knownGKVs = sets.Set[schema.GroupVersionKind]{
		podGVK:            {},
		secretGVK:         {},
		serviceGVK:        {},
		configMapGVK:      {},
		pvcGVK:            {},
		deploymentGVK:     {},
		replicaSetGVK:     {},
		statefulSetGVK:    {},
		jobGVK:            {},
		cronJobGVK:        {},
		storageRequestGVK: {},
	}
)

func (r *Reconciler) ensureApplyPrerequisites(ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	objectMutators objectMutatorSet,
	genericMutator objectMutator,
	workerPullSecrets, workloadPullSecrets []*corev1.Secret,
) error {
	targetNamespace := ms.Spec.Namespace

	if err := r.ensureMiniServiceRBAC(ctx, targetNamespace); err != nil {
		return err
	}

	if err := r.ensureNetworkPolicies(ctx, targetNamespace); err != nil {
		return err
	}

	if err := r.ensureResourceConstraints(ctx, targetNamespace, icmsReq); err != nil {
		return err
	}

	// Create the pull secret before storage requests so SMB server can be pulled.
	workerPullSecretObjs := make([]client.Object, len(workerPullSecrets)+len(workloadPullSecrets))
	for i, pullSecret := range append(workerPullSecrets, workloadPullSecrets...) {
		workerPullSecretObjs[i] = pullSecret
	}
	if err := r.applyInfra(ctx, ms, objectMutators, genericMutator, workerPullSecretObjs...); err != nil {
		return err
	}

	if err := r.updateServiceAccountImagePullSecrets(ctx, targetNamespace, defaultServiceAccountName, workerPullSecrets); err != nil {
		return fmt.Errorf("update default service account with worker image pull secrets: %w", err)
	}

	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmRBACEnforcement) {
		// Workload pods often have infra containers injected that require worker pull secrets.
		if err := r.updateServiceAccountImagePullSecrets(ctx, targetNamespace, serviceAccountName,
			append(workloadPullSecrets, workerPullSecrets...)); err != nil {
			return fmt.Errorf("update %s service account with workload image pull secrets: %w", serviceAccountName, err)
		}
	}

	return nil
}

var noMF = func() error { return nil }

func (r *Reconciler) ensureMiniServiceRBAC(ctx context.Context, targetNamespace string) error {
	if !r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmRBACEnforcement) {
		return nil
	}
	rbacCM := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: r.SystemNamespace, Name: instanceRBACConfigMapName}
	if err := r.Client.Get(ctx, key, rbacCM); err != nil {
		return err
	}
	return r.ensureMiniServiceRBACFromConfigMap(ctx, targetNamespace, rbacCM)
}

func (r *Reconciler) ensureMiniServiceRBACFromConfigMap(ctx context.Context,
	targetNamespace string,
	rbacCM *corev1.ConfigMap,
) error {
	instanceSA := &corev1.ServiceAccount{}
	instanceSA.Name = serviceAccountName
	instanceSA.Namespace = targetNamespace
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, instanceSA, noMF); err != nil {
		return fmt.Errorf("ensure instance ServiceAccount: %v", err)
	}

	roleYAML, ok := rbacCM.Data[instanceRoleName]
	if !ok {
		return fmt.Errorf("expected ConfigMap %s key %s to exist, was empty",
			instanceRBACConfigMapName, instanceRoleName)
	}

	const instanceRName = serviceAccountName
	instanceRole := &rbacv1.Role{}
	if _, _, err := r.Decoder.Decode([]byte(roleYAML), nil, instanceRole); err != nil {
		return fmt.Errorf("unmarshal instance Role %s: %v", instanceRoleName, err)
	}
	instanceRole.Name = instanceRName
	instanceRole.Namespace = targetNamespace

	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, instanceRole, noMF); err != nil {
		return fmt.Errorf("ensure instance Role: %v", err)
	}

	const instanceRBName = serviceAccountName
	instanceRB := &rbacv1.RoleBinding{}
	instanceRB.Name = instanceRBName
	instanceRB.Namespace = targetNamespace
	instanceRB.RoleRef = rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     instanceRole.Name,
	}
	instanceRB.Subjects = []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      instanceSA.Name,
		Namespace: targetNamespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, instanceRB, noMF); err != nil {
		return fmt.Errorf("ensure instance RoleBinding: %v", err)
	}

	return nil
}

func (r *Reconciler) ensureNetworkPolicies(ctx context.Context, namespace string) error {
	npCM := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: r.SystemNamespace, Name: k8sutil.NetworkPoliciesConfigMapName}
	if err := r.Client.Get(ctx, key, npCM); err != nil {
		return err
	}
	var extraNPs []*netv1.NetworkPolicy
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(&featureflag.HelmSharedStorage.FeatureFlag) ||
		r.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrOVCSecurityEnforcements) {
		extraNPs = append(extraNPs, storage.GetIngressNetworkPolicies()...)
	}
	return k8sutil.EnsureNetworkPoliciesFunctionNamespace(ctx, namespace, npCM.Data, r.FeatureFlagFetcher, nil, r.Client, extraNPs...)
}

func setInfraValues(values json.RawMessage, icmsReq *nvcav2beta1.ICMSRequest) (json.RawMessage, error) {
	newValues := map[string]any{}

	if taskLaunchSpec := icmsReq.Spec.CreationMsgInfo.TaskLaunchSpecification; taskLaunchSpec != nil {
		_, taskName, err := getFunctionNameAndTaskName(nil, taskLaunchSpec)
		if err != nil {
			return nil, err
		}
		for _, val := range [][2]string{
			{"nvctNcaId", icmsReq.Spec.NCAId},
			{"nvctTaskId", icmsReq.Spec.TaskDetails.TaskID},
			{"nvctTaskName", taskName},
			{"nvctResultsDir", common.NVCTTaskResultsDir},
			{"nvctProgressFilePath", common.NVCTTaskProgressFilePath},
		} {
			newValues[val[0]] = val[1]
		}
	}

	if len(newValues) == 0 {
		return values, nil
	}

	var valuesMap map[string]any
	if len(values) != 0 {
		// Do not overwrite existing values.
		valuesMap = map[string]any{}
		if err := json.Unmarshal(values, &valuesMap); err != nil {
			return nil, fmt.Errorf("decode existing Helm values JSON: %v", err)
		}
		if err := mergo.Merge(&valuesMap, newValues); err != nil {
			return nil, fmt.Errorf("merge pull secret values with existing: %v", err)
		}
	} else {
		valuesMap = newValues
	}
	values, err := json.Marshal(valuesMap)
	if err != nil {
		return nil, fmt.Errorf("encode modified Helm values JSON: %v", err)
	}

	return values, nil
}

func (r *Reconciler) ensureResourceConstraints(ctx context.Context, namespace string, req *nvcav2beta1.ICMSRequest) error {
	// Overhead is added to reqs/lims to account for other containers/custom infra appended to Pods in the namespace.
	overhead, err := r.OverheadGetter.GetInfraOverhead(ctx)
	if err != nil {
		return err
	}

	cmInfo := req.Spec.CreationMsgInfo
	computeReqs, computeLims, err := r.calculatePodInstanceResourcesForInstanceType(cmInfo, overhead)
	if err != nil {
		return fmt.Errorf("calculate resource constraints: %v", err)
	}

	return k8sutil.EnsureResourceQuotas(ctx,
		r.FeatureFlagFetcher,
		k8sutil.NewControllerRuntimeClientShim(r.Client),
		req.Spec.Action,
		namespace,
		computeReqs, computeLims,
	)
}

func (r *Reconciler) calculatePodInstanceResourcesForInstanceType(
	cmInfo nvcav2beta1.ICMSCreationMessageInfo,
	overhead corev1.ResourceList,
) (reqs, lims corev1.ResourceList, err error) {
	instanceType, ok := r.regITCache.Get(cmInfo.InstanceTypeName)
	if !ok {
		return nil, nil, fmt.Errorf("instance type not found with name: %s", cmInfo.InstanceTypeName)
	}
	gpuResName := nodefeatures.GetGPUResourceNameFetcher(r.FeatureFlagFetcher)
	return nvcatypes.CalculateResourcesForInstanceType(instanceType, gpuResName, overhead)
}

func (r *Reconciler) updateServiceAccountImagePullSecrets(
	ctx context.Context,
	namespace, saName string,
	imagePullSecrets []*corev1.Secret,
) error {
	log := logf.FromContext(ctx).WithValues("sa", saName, "namespace", namespace)

	updated, err := k8sutil.UpdateServiceAccountImagePullSecrets(ctx, nil, r.Client, namespace, saName, imagePullSecrets)
	if err != nil {
		log.Error(err, "Failed to update instance service account with image pull secrets")
		return err
	}

	if updated {
		log.Info("Updated service account with image pull secrets")
	}

	return nil
}

func (r *Reconciler) ensureImageCredentialUpdaterObjects(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	objectMutators objectMutatorSet,
) (bool, error) {
	log := logf.FromContext(ctx)

	if r.ImageCredentialHelperImage == "" {
		log.V(1).Info("Third party registry image credential helper image not configured, skipping image credential update setup")
		return false, nil
	}

	allEnvSet, err := parseWorkloadEnvSet(icmsReq)
	if err != nil {
		log.Error(err, "Failed to decode workload env set")
		return false, reconcile.TerminalError(err)
	}

	if allEnvSet[common.ContainerRegistriesCredentialsEnv] == "" || allEnvSet[common.SidecarRegistryCredentialEnv] == "" {
		log.V(1).Info("Third party registry support not configured for this function, skipping image credential update setup")
		return false, nil
	}

	// Initial creds created in the instance's namespace.
	tprCredSecret, err := imagecredential.NewImageCredsSecret(icmsReq.Name, allEnvSet)
	if err != nil {
		log.Error(err, "Failed to create third party registry cred helper objects")
		return false, reconcile.TerminalError(err)
	}
	if err := r.applyInfra(ctx, ms, objectMutators, nil, tprCredSecret); err != nil {
		return false, err
	}

	// System job to init creds before images need to be pulled.
	tprUpdaterInitJob := imagecredential.NewInitJob(icmsReq.Name+"-cred-init", r.ImageCredentialHelperImage, ms.Spec.Namespace, "")
	tprUpdaterInitJob.Namespace = r.SystemNamespace
	// Since the job is an infra object in the system namespace, it should not get worker labels.
	jobLabels := map[string]string{
		miniserviceNameLabel: ms.Name,
	}
	tprUpdaterInitJob.Labels = jobLabels
	tprUpdaterInitJob.Spec.Template.Labels = jobLabels
	// Use NVCA's service account to run the job for API access and image pull secrets.
	tprUpdaterInitJob.Spec.Template.Spec.ServiceAccountName = "nvca"
	k8sutil.MergePodSpecTolerations(&tprUpdaterInitJob.Spec.Template.Spec, r.cfg.Workload.Tolerations...)

	// All Pods created by NVCA must use the same scheduler.
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		tprUpdaterInitJob.Spec.Template.Spec.SchedulerName = kaischeduler.SchedulerName
		tprUpdaterInitJob.Spec.Template.Labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.DefaultQueue
	}

	// Set TTL to 1 hour so jobs are cleaned up in case of terminal failure
	// or the instance is cleaned up before this check completes.
	tprUpdaterInitJob.Spec.TTLSecondsAfterFinished = new(int32)
	*tprUpdaterInitJob.Spec.TTLSecondsAfterFinished = 3600

	// These jobs should complete in seconds. If some issue results in jobs running for > 10 minutes,
	// their pods should be terminated and the job should be marked failed.
	tprUpdaterInitJob.Spec.ActiveDeadlineSeconds = new(int64)
	*tprUpdaterInitJob.Spec.ActiveDeadlineSeconds = 600

	tmpJob := &batchv1.Job{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(tprUpdaterInitJob), tmpJob); err == nil {
		// Clear task label so checker ensures pods have completed.
		delete(tmpJob.Labels, nvcatypes.TaskIDKey)
		delete(tmpJob.Labels, nvcatypes.TaskIDUpperKey)
		log.V(1).Info("Checking image pull updater init job status")
		jobCtx, err := r.buildStatusContext(ctx, tmpJob.Namespace)
		if err != nil {
			return false, err
		}
		objStatus, err := r.checkJobStatus(jobCtx, tmpJob, icmsReq)
		if err != nil {
			log.Error(err, "Failed to check image pull updater init job status")
			return false, err
		}
		switch {
		case objStatus.Status == statusSucceeded:
			log.V(1).Info("Image pull updater init job succeeded")
			if tmpJob.Annotations == nil {
				tmpJob.Annotations = map[string]string{}
			}
			tmpJob.Annotations[k8sutil.ImageCredUpdaterInitJobCompletedAnnotationKey] = "true"
			if err := r.Client.Update(ctx, tmpJob); err != nil {
				return false, err
			}
		case objStatus.TerminalBad:
			var events []string
			for _, e := range objStatus.AbnormalEvents {
				events = append(events, e.Message)
			}
			var msg string
			if objStatus.TerminalBad {
				msg = objStatus.Reason
			} else {
				msg = "error events encountered"
			}
			err := fmt.Errorf("image pull updater init job failed: %s", msg)
			log.WithValues("events", events).Error(err, "Image pull updater init job in terminal state")
			return false, reconcile.TerminalError(err)
		default:
			log.V(1).Info("Image pull updater init job is pending or running, requeuing")
			return true, nil
		}
	} else if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, tprUpdaterInitJob); err != nil {
			log.Error(err, "Failed to create image pull updater init job")
			return false, err
		}
		log.V(1).Info("Image pull updater init job has been applied, requeuing")
		return true, nil
	} else {
		log.Error(err, "Failed to get image pull updater init job")
		return false, err
	}

	return false, nil
}
