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

package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/controlplane"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type controlPlaneAdoptionResult struct {
	Adopted    []string
	WouldAdopt []string
}

func (r *controlPlaneAdoptionResult) record(ref string, dryRun bool) {
	if containsAdoptionRef(r.Adopted, ref) || containsAdoptionRef(r.WouldAdopt, ref) {
		return
	}
	if dryRun {
		r.WouldAdopt = append(r.WouldAdopt, ref)
		return
	}
	r.Adopted = append(r.Adopted, ref)
}

func (r *controlPlaneAdoptionResult) changedRefs() []string {
	refs := append([]string{}, r.Adopted...)
	refs = append(refs, r.WouldAdopt...)
	return refs
}

func (r *controlPlaneAdoptionResult) summary() string {
	refs := r.changedRefs()
	if len(refs) == 0 {
		return "none"
	}
	return strings.Join(refs, ", ")
}

func containsAdoptionRef(refs []string, ref string) bool {
	for _, existing := range refs {
		if existing == ref {
			return true
		}
	}
	return false
}

func (bc *BackendK8sCache) adoptLegacyControlPlaneObjects(
	ctx context.Context,
	dryRun bool,
) (*controlPlaneAdoptionResult, error) {
	if bc == nil || bc.clients == nil || bc.clients.K8s == nil {
		return nil, fmt.Errorf("kubernetes client is required for control-plane adoption")
	}

	identity := bc.controlPlaneIdentityOrDefault()
	if !identity.Valid() {
		return nil, fmt.Errorf("invalid control plane identity %q", identity.String())
	}

	result := &controlPlaneAdoptionResult{}
	if !identity.IsDefault() {
		core.GetLogger(ctx).WithField("controlPlaneIdentity", identity.String()).
			Info("Skipping legacy control-plane object adoption for named control plane")
		return result, nil
	}

	k8sClient := bc.clients.K8s
	for _, namespace := range []string{
		DefaultNVCASystemNamespace,
		DefaultNVCARequestsNamespace,
		storage.ModelCacheInitNamespace,
	} {
		if err := adoptLegacyNamespaceByName(ctx, k8sClient, identity, namespace, dryRun, result); err != nil {
			return nil, err
		}
	}

	if err := adoptLegacyWorkloadNamespaces(ctx, k8sClient, identity, dryRun, result); err != nil {
		return nil, err
	}

	clusterScopedName, err := bc.controlPlaneResourceName(nvcaoptypes.NVCAModuleName)
	if err != nil {
		return nil, fmt.Errorf("failed to build legacy control-plane resource name: %w", err)
	}
	if err := adoptLegacyServiceAccount(ctx, k8sClient, identity, DefaultNVCASystemNamespace,
		nvcaoptypes.NVCAModuleName, dryRun, result); err != nil {
		return nil, err
	}
	if err := adoptLegacyValidatingWebhookConfiguration(ctx, k8sClient, identity, clusterScopedName, dryRun, result); err != nil {
		return nil, err
	}
	if err := adoptLegacyMutatingWebhookConfiguration(ctx, k8sClient, identity, clusterScopedName, dryRun, result); err != nil {
		return nil, err
	}
	if err := adoptLegacyClusterRole(ctx, k8sClient, identity, clusterScopedName, dryRun, result); err != nil {
		return nil, err
	}
	if err := adoptLegacyClusterRoleBinding(ctx, k8sClient, identity, clusterScopedName, dryRun, result); err != nil {
		return nil, err
	}

	log := core.GetLogger(ctx).WithField("objects", result.summary())
	if dryRun {
		log.Infof("Control-plane adoption dry-run complete")
	} else {
		log.Infof("Control-plane adoption complete")
	}
	return result, nil
}

func adoptLegacyNamespaceByName(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("Namespace/%s", name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ns, err := k8sClient.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy namespace %q: %w", name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, ns.Labels, dryRun, result, func(labels map[string]string) error {
			updated := ns.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.CoreV1().Namespaces().Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyWorkloadNamespaces(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	// Workload namespace names are request-derived, so the legacy workload label
	// is the durable signal that NVCA created the namespace.
	nsList, err := k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: nvcatypes.WorkloadInstanceTypeLabel,
	})
	if err != nil {
		return fmt.Errorf("failed to list legacy workload namespaces: %w", err)
	}

	sort.Slice(nsList.Items, func(i, j int) bool {
		return nsList.Items[i].Name < nsList.Items[j].Name
	})
	for _, ns := range nsList.Items {
		if err := adoptLegacyNamespaceByName(ctx, k8sClient, identity, ns.Name, dryRun, result); err != nil {
			return err
		}
	}
	return nil
}

func adoptLegacyServiceAccount(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	namespace string,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("ServiceAccount/%s/%s", namespace, name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sa, err := k8sClient.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy serviceaccount %q/%q: %w", namespace, name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, sa.Labels, dryRun, result, func(labels map[string]string) error {
			updated := sa.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.CoreV1().ServiceAccounts(namespace).Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyValidatingWebhookConfiguration(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("ValidatingWebhookConfiguration/%s", name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		webhook, err := k8sClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy validatingwebhookconfiguration %q: %w", name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, webhook.Labels, dryRun, result, func(labels map[string]string) error {
			updated := webhook.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().
				Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyMutatingWebhookConfiguration(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("MutatingWebhookConfiguration/%s", name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		webhook, err := k8sClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy mutatingwebhookconfiguration %q: %w", name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, webhook.Labels, dryRun, result, func(labels map[string]string) error {
			updated := webhook.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.AdmissionregistrationV1().MutatingWebhookConfigurations().
				Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyClusterRole(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("ClusterRole/%s", name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterRole, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy clusterrole %q: %w", name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, clusterRole.Labels, dryRun, result, func(labels map[string]string) error {
			updated := clusterRole.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.RbacV1().ClusterRoles().Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyClusterRoleBinding(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	identity controlplane.Identity,
	name string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
) error {
	ref := fmt.Sprintf("ClusterRoleBinding/%s", name)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterRoleBinding, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to get legacy clusterrolebinding %q: %w", name, err)
		}
		return adoptLegacyObject(ctx, identity, ref, clusterRoleBinding.Labels, dryRun, result, func(labels map[string]string) error {
			updated := clusterRoleBinding.DeepCopy()
			updated.Labels = labels
			_, err := k8sClient.RbacV1().ClusterRoleBindings().Update(ctx, updated, metav1.UpdateOptions{})
			return err
		})
	})
}

func adoptLegacyObject(
	ctx context.Context,
	identity controlplane.Identity,
	ref string,
	objectLabels map[string]string,
	dryRun bool,
	result *controlPlaneAdoptionResult,
	update func(map[string]string) error,
) error {
	if objectLabels[controlplane.OwnerLabel] != "" {
		return nil
	}
	if dryRun {
		result.record(ref, true)
		return nil
	}

	labels := make(map[string]string, len(objectLabels)+1)
	for key, value := range objectLabels {
		labels[key] = value
	}
	labels[controlplane.OwnerLabel] = identity.String()

	if err := update(labels); err != nil {
		return fmt.Errorf("failed to adopt legacy %s: %w", ref, err)
	}
	result.record(ref, false)
	core.GetLogger(ctx).WithField("object", ref).Info("Adopted legacy control-plane object")
	return nil
}
