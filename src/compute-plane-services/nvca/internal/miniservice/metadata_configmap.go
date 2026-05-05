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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// MetadataInput holds all data needed to build a MiniserviceMetadata.
type MetadataInput struct {
	FunctionName                  string
	TaskName                      string
	Tolerations                   []corev1.Toleration
	ImagePullSecrets              []*corev1.Secret
	GeneralAnnotations            map[string]string
	GeneralLabels                 map[string]string
	PodAnnotations                map[string]string
	PodLabels                     map[string]string
	EnvVars                       []corev1.EnvVar
	TerminationGracePeriodSeconds *int64
}

// buildMiniserviceMetadata constructs a MiniserviceMetadata from the controller's
// request context. The resulting struct populates the nvcf-miniservice-metadata
// ConfigMap that the mutating webhook reads at admission time.
func (r *Reconciler) buildMiniserviceMetadata(
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	in MetadataInput,
) (nvcatypes.MiniserviceMetadata, error) {
	secretNames := make([]string, 0, len(in.ImagePullSecrets))
	for _, s := range in.ImagePullSecrets {
		secretNames = append(secretNames, s.Name)
	}

	envVars := append(
		common.MakeWorkloadEnvVars(icmsReq.Spec.Action),
		newInstanceIDEnv(icmsReq.Spec.Action),
	)
	envVars = append(envVars, in.EnvVars...)

	meta := nvcatypes.MiniserviceMetadata{
		MessageAction:                 icmsReq.Spec.Action.Normalize(),
		Annotations:                   in.GeneralAnnotations,
		Labels:                        in.GeneralLabels,
		PodAnnotations:                in.PodAnnotations,
		PodLabels:                     in.PodLabels,
		EnvVars:                       envVars,
		NodeAffinityKey:               nodefeatures.UniformInstanceTypeLabelKey,
		NodeAffinityValue:             icmsReq.Spec.CreationMsgInfo.GetInstanceTypeLabelSelValue(),
		ServiceAccountName:            serviceAccountName,
		Tolerations:                   r.cfg.Workload.Tolerations,
		ImagePullSecretNames:          secretNames,
		TerminationGracePeriodSeconds: in.TerminationGracePeriodSeconds,
	}

	meta.Labels, meta.Annotations = newGeneralObjectLabelsAndAnnotations(
		r.FeatureFlagFetcher, ms, icmsReq,
		r.ClusterRegion, r.ClusterName, in.FunctionName, in.TaskName, false,
	)

	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		meta.SchedulerName = kaischeduler.SchedulerName
		kaiSchedulerQueueName := kaischeduler.GetQName()
		if meta.PodLabels == nil {
			meta.PodLabels = make(map[string]string)
		}
		meta.PodLabels[kaischeduler.SchedulerQueueLabel] = kaiSchedulerQueueName
	}

	return meta, nil
}

// ensureMiniserviceMetadataConfigMap creates the nvcf-miniservice-metadata
// ConfigMap in the MiniService's instance namespace. The webhook's Fail-policy
// CREATE rule requires this ConfigMap to exist before any object is admitted
// into the namespace.
func (r *Reconciler) ensureMiniserviceMetadataConfigMap(
	ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	mi MetadataInput,
) error {
	log := logf.FromContext(ctx)

	cmKey := client.ObjectKey{Namespace: ms.Spec.Namespace, Name: nvcatypes.MiniserviceMetadataConfigMapName}
	if err := r.Client.Get(ctx, cmKey, &corev1.ConfigMap{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get miniservice metadata ConfigMap: %w", err)
	} else if err == nil {
		return nil
	}

	meta, err := r.buildMiniserviceMetadata(ms, icmsReq, mi)
	if err != nil {
		return fmt.Errorf("build miniservice metadata: %w", err)
	}

	data, err := meta.ToConfigMapData()
	if err != nil {
		return fmt.Errorf("serialize miniservice metadata: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nvcatypes.MiniserviceMetadataConfigMapName,
			Namespace: ms.Spec.Namespace,
			Labels: map[string]string{
				miniserviceNameLabel: ms.Name,
			},
		},
		Data: data,
	}
	if err := r.Client.Create(ctx, cm); err != nil {
		return fmt.Errorf("create miniservice metadata ConfigMap: %w", err)
	}
	log.V(1).Info("Created miniservice metadata ConfigMap")

	return nil
}
