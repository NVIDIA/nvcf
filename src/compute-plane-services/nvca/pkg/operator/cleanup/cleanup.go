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

// Package cleanup provides shared cleanup functions for the nvca-operator.
// These functions are used by both the main operator during normal NVCFBackend
// deletion and by the preStop hook during operator shutdown.
package cleanup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

// Re-export constants from types package for backward compatibility
const (
	NVCAOperatorFinalizer          = nvcaoptypes.NVCAOperatorFinalizer
	SentinelFinalizer              = nvcaoptypes.SentinelFinalizer
	ShutdownSentinelConfigMapName  = nvcaoptypes.ShutdownSentinelConfigMapName
	NVCAModuleName                 = nvcaoptypes.NVCAModuleName
	NVCAOperatorName               = nvcaoptypes.NVCAOperatorName
	DefaultNVCASystemNamespace     = nvcaoptypes.DefaultNVCASystemNamespace
	DefaultNVCARequestsNamespace   = nvcaoptypes.DefaultNVCARequestsNamespace
	DefaultModelCacheInitNamespace = storage.ModelCacheInitNamespace

	// CordonAndDrainMaintenanceFeatureFlag is the feature flag that triggers NVCA to drain workloads
	CordonAndDrainMaintenanceFeatureFlag = "CordonAndDrainMaintenance"
)

// CleanupOption is a functional option for configuring cleanup behavior
type CleanupOption func(*cleanupOptions) //nolint:revive // exported name is intentional

// cleanupOptions configures cleanup behavior
type cleanupOptions struct {
}

// BackendNamespaces returns the system and requests namespace names for an NVCFBackend
func BackendNamespaces(nb *nvidiaiov1.NVCFBackend) (systemNS, requestsNS string) {
	systemNS = DefaultNVCASystemNamespace
	if nb.Spec.ClusterConfig.SystemNamespace != "" {
		systemNS = nb.Spec.ClusterConfig.SystemNamespace
	}

	requestsNS = DefaultNVCARequestsNamespace
	if nb.Spec.ClusterConfig.RequestsNamespace != "" {
		requestsNS = nb.Spec.ClusterConfig.RequestsNamespace
	}

	return systemNS, requestsNS
}

// CleanupBackendResources deletes all resources created by an NVCFBackend
// including namespaces, webhooks, and cluster roles.
// Note: Operator-managed CRDs are cleaned up via owner references.
func CleanupBackendResources( //nolint:revive // exported name is intentional
	ctx context.Context,
	k8sClient kubernetes.Interface,
	dynamicClient dynamic.Interface,
	nb *nvidiaiov1.NVCFBackend,
	options ...CleanupOption,
) error {
	opts := &cleanupOptions{}
	for _, o := range options {
		o(opts)
	}

	log := core.GetLogger(ctx)
	log.Infof("cleaning-up resources for nvcfbackend %v/%v", nb.Namespace, nb.Name)

	systemNS, requestsNS := BackendNamespaces(nb)

	// Delete all ICMSRequest CRs (remove finalizers first, then delete)
	if err := deleteICMSRequests(ctx, dynamicClient, requestsNS); err != nil {
		log.WithError(err).Warnf("failed to delete ICMS requests in namespace %s", requestsNS)
	}

	// Delete workload namespaces (sr-*) that NVCA created for each request.
	// These are standalone top-level namespaces with no owner references, so they
	// won't be cleaned up by garbage collection. Normally NVCA's MiniService controller
	// handles this, but during forced cleanup NVCA is being torn down.
	if err := deleteWorkloadNamespaces(ctx, k8sClient); err != nil {
		log.WithError(err).Warn("failed to delete some workload namespaces")
	}

	// Cleanup the system namespace
	err := k8sClient.CoreV1().Namespaces().Delete(ctx, systemNS, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to cleanup namespace %v, err: %v", systemNS, err)
	}

	// Cleanup the requests namespace
	err = k8sClient.CoreV1().Namespaces().Delete(ctx, requestsNS, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to cleanup namespace %v, err: %v", requestsNS, err)
	}

	// Cleanup the shared model-cache initialization namespace created by NVCA.
	err = k8sClient.CoreV1().Namespaces().Delete(ctx, DefaultModelCacheInitNamespace, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to cleanup namespace %v, err: %v", DefaultModelCacheInitNamespace, err)
	}

	// Delete ValidatingWebhookConfiguration
	err = k8sClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, NVCAModuleName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete validatingwebhookconfiguration %v, err: %v", NVCAModuleName, err)
	}

	// Delete MutatingWebhookConfiguration
	err = k8sClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, NVCAModuleName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete mutatingwebhookconfiguration %v, err: %v", NVCAModuleName, err)
	}

	// Delete ClusterRole
	err = k8sClient.RbacV1().ClusterRoles().Delete(ctx, NVCAModuleName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete cluster-role %v, err: %v", NVCAModuleName, err)
	}

	// Delete ClusterRoleBinding
	err = k8sClient.RbacV1().ClusterRoleBindings().Delete(ctx, NVCAModuleName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete cluster-role-bindings %v, err: %v", NVCAModuleName, err)
	}

	// Note: Operator-managed CRDs (ICMSRequest, StorageRequest, MiniServices) have owner references
	// to the NVCFBackend CRD and will be garbage collected when Helm deletes that CRD.

	log.Infof("Successfully cleaned up resources for nvcfbackend %v/%v", nb.Namespace, nb.Name)
	return nil
}

// DeleteNVCFBackend deletes an NVCFBackend resource
func DeleteNVCFBackend(
	ctx context.Context,
	nvcaClient versioned.Interface,
	nb *nvidiaiov1.NVCFBackend,
) error {
	log := core.GetLogger(ctx)
	log.Debugf("Deleting NVCFBackend %v/%v", nb.Namespace, nb.Name)

	err := nvcaClient.NvcfV1().NVCFBackends(nb.Namespace).Delete(ctx, nb.Name, metav1.DeleteOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.Debugf("NVCFBackend %v/%v already deleted", nb.Namespace, nb.Name)
			return nil
		}
		log.WithError(err).Errorf("Failed to delete NVCFBackend %v/%v", nb.Namespace, nb.Name)
		return err
	}

	log.Debugf("Deleted NVCFBackend %v/%v successfully", nb.Namespace, nb.Name)
	return nil
}

// finalizerRemover defines the interface for removing a finalizer from a Kubernetes object
type finalizerRemover struct {
	getFinalizers func() ([]string, error)
	setFinalizers func([]string) error
}

// removeFinalizer is a generic helper that removes a finalizer from any Kubernetes resource
func removeFinalizer(ctx context.Context, resourceName, finalizer string, remover finalizerRemover) error {
	log := core.GetLogger(ctx)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		finalizers, err := remover.getFinalizers()
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		if !containsString(finalizers, finalizer) {
			return nil
		}

		if err := remover.setFinalizers(removeString(finalizers, finalizer)); err != nil {
			return err
		}

		log.Infof("Removed finalizer %s from %s", finalizer, resourceName)
		return nil
	})
}

// RemoveNVCFBackendFinalizer removes the operator finalizer from an NVCFBackend
func RemoveNVCFBackendFinalizer(
	ctx context.Context,
	nvcaClient versioned.Interface,
	nb *nvidiaiov1.NVCFBackend,
) error {
	return removeFinalizer(ctx, fmt.Sprintf("NVCFBackend %s/%s", nb.Namespace, nb.Name), NVCAOperatorFinalizer, finalizerRemover{
		getFinalizers: func() ([]string, error) {
			latest, err := nvcaClient.NvcfV1().NVCFBackends(nb.Namespace).Get(ctx, nb.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return latest.Finalizers, nil
		},
		setFinalizers: func(f []string) error {
			latest, err := nvcaClient.NvcfV1().NVCFBackends(nb.Namespace).Get(ctx, nb.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			latest.Finalizers = f
			_, err = nvcaClient.NvcfV1().NVCFBackends(nb.Namespace).Update(ctx, latest, metav1.UpdateOptions{})
			return err
		},
	})
}

// RemoveSentinelFinalizer removes the shutdown finalizer from the sentinel ConfigMap
func RemoveSentinelFinalizer(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	namespace, name, finalizer string,
) error {
	return removeFinalizer(ctx, fmt.Sprintf("ConfigMap %s/%s", namespace, name), finalizer, finalizerRemover{
		getFinalizers: func() ([]string, error) {
			cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return cm.Finalizers, nil
		},
		setFinalizers: func(f []string) error {
			cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			cm.Finalizers = f
			_, err = k8sClient.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
			return err
		},
	})
}

// RemoveRBACFinalizers removes the cleanup finalizer from RBAC resources
// (ClusterRole, ClusterRoleBinding, ServiceAccount).
//
// Migration note: as of the SA-finalizer-deadlock fix, the chart no longer
// adds the operator-cleanup finalizer to the operator's ServiceAccount (see
// templates/sa.yaml). The ClusterRole and ClusterRoleBinding still carry
// the finalizer. The ServiceAccount branch below is kept so upgrades from
// charts that previously added the finalizer still get it cleaned up at
// the next operator preStop run; on fresh installs the SA branch is a
// graceful no-op via removeFinalizer's "finalizer not present" short-
// circuit (see removeFinalizer).
func RemoveRBACFinalizers(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	clusterRoleName, clusterRoleBindingName, serviceAccountName, serviceAccountNamespace, finalizer string,
) error {
	log := core.GetLogger(ctx)
	var errs []error

	// Remove finalizer from ClusterRole
	if clusterRoleName != "" {
		if err := removeClusterRoleFinalizer(ctx, k8sClient, clusterRoleName, finalizer); err != nil {
			log.WithError(err).Errorf("Failed to remove finalizer from ClusterRole %s", clusterRoleName)
			errs = append(errs, err)
		}
	}

	// Remove finalizer from ClusterRoleBinding
	if clusterRoleBindingName != "" {
		if err := removeClusterRoleBindingFinalizer(ctx, k8sClient, clusterRoleBindingName, finalizer); err != nil {
			log.WithError(err).Errorf("Failed to remove finalizer from ClusterRoleBinding %s", clusterRoleBindingName)
			errs = append(errs, err)
		}
	}

	// Remove finalizer from ServiceAccount
	if serviceAccountName != "" {
		if err := removeServiceAccountFinalizer(ctx, k8sClient, serviceAccountNamespace, serviceAccountName, finalizer); err != nil {
			log.WithError(err).Errorf("Failed to remove finalizer from ServiceAccount %s/%s", serviceAccountNamespace, serviceAccountName)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to remove %d RBAC finalizer(s)", len(errs))
	}

	log.Info("Successfully removed finalizers from all RBAC resources")
	return nil
}

func removeClusterRoleFinalizer(ctx context.Context, k8sClient kubernetes.Interface, name, finalizer string) error {
	return removeFinalizer(ctx, fmt.Sprintf("ClusterRole %s", name), finalizer, finalizerRemover{
		getFinalizers: func() ([]string, error) {
			cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return cr.Finalizers, nil
		},
		setFinalizers: func(f []string) error {
			cr, err := k8sClient.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			cr.Finalizers = f
			_, err = k8sClient.RbacV1().ClusterRoles().Update(ctx, cr, metav1.UpdateOptions{})
			return err
		},
	})
}

func removeClusterRoleBindingFinalizer(ctx context.Context, k8sClient kubernetes.Interface, name, finalizer string) error {
	return removeFinalizer(ctx, fmt.Sprintf("ClusterRoleBinding %s", name), finalizer, finalizerRemover{
		getFinalizers: func() ([]string, error) {
			crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return crb.Finalizers, nil
		},
		setFinalizers: func(f []string) error {
			crb, err := k8sClient.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			crb.Finalizers = f
			_, err = k8sClient.RbacV1().ClusterRoleBindings().Update(ctx, crb, metav1.UpdateOptions{})
			return err
		},
	})
}

// removeServiceAccountFinalizer is a migration helper that strips the
// operator-cleanup finalizer from a ServiceAccount that an older chart
// version (pre-SA-finalizer-deadlock fix) added. The current chart
// (templates/sa.yaml) does not add this finalizer at install time, so on
// fresh installs this is a no-op via removeFinalizer's missing-finalizer
// short-circuit. Kept so an in-place chart upgrade leaves no orphaned
// finalizer behind.
func removeServiceAccountFinalizer(ctx context.Context, k8sClient kubernetes.Interface, namespace, name, finalizer string) error {
	return removeFinalizer(ctx, fmt.Sprintf("ServiceAccount %s/%s", namespace, name), finalizer, finalizerRemover{
		getFinalizers: func() ([]string, error) {
			sa, err := k8sClient.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return sa.Finalizers, nil
		},
		setFinalizers: func(f []string) error {
			sa, err := k8sClient.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			sa.Finalizers = f
			_, err = k8sClient.CoreV1().ServiceAccounts(namespace).Update(ctx, sa, metav1.UpdateOptions{})
			return err
		},
	})
}

// IsSentinelBeingDeleted checks if the sentinel ConfigMap is being deleted
func IsSentinelBeingDeleted(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	namespace, name string,
) (bool, error) {
	cm, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Sentinel doesn't exist, treat as being deleted
			return true, nil
		}
		return false, fmt.Errorf("failed to get sentinel configmap: %v", err)
	}

	return cm.DeletionTimestamp != nil, nil
}

// CountICMSRequests returns the count of ICMSRequest CRs in a namespace.
func CountICMSRequests(ctx context.Context, dynamicClient dynamic.Interface, namespace string) (int, error) {
	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	list, err := dynamicClient.Resource(icmsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to list ICMS requests: %w", err)
	}

	return len(list.Items), nil
}

const (
	agentConfigConfigMapName = "agent-config"
	agentConfigKey           = "config.yaml"
)

// PatchNVCAMaintenanceMode directly patches the NVCA agent-config ConfigMap to set maintenanceMode
// and add the CordonAndDrainMaintenance feature flag. This triggers a rollout restart of the NVCA
// deployment. This is used during graceful shutdown when a full reconciliation would fail due to
// missing helm-managed resources.
func PatchNVCAMaintenanceMode(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	systemNamespace string,
	maintenanceMode string,
) error {
	log := core.GetLogger(ctx)

	// Get the agent-config ConfigMap
	cm, err := k8sClient.CoreV1().ConfigMaps(systemNamespace).Get(ctx, agentConfigConfigMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get agent-config ConfigMap: %w", err)
	}

	// Get the config.yaml content
	configYAML, ok := cm.Data[agentConfigKey]
	if !ok {
		return fmt.Errorf("agent-config ConfigMap missing %s key", agentConfigKey)
	}

	// Patch the config YAML:
	// 1. Add CordonAndDrainMaintenance feature flag if not present
	// 2. Set maintenanceMode
	newConfigYAML := addFeatureFlagToConfig(configYAML, CordonAndDrainMaintenanceFeatureFlag)
	newConfigYAML = addMaintenanceModeToConfig(newConfigYAML, maintenanceMode)

	// Update the ConfigMap
	cm.Data[agentConfigKey] = newConfigYAML
	_, err = k8sClient.CoreV1().ConfigMaps(systemNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update agent-config ConfigMap: %w", err)
	}

	log.Infof("Patched agent-config ConfigMap in %s with featureFlag=%s and maintenanceMode=%s",
		systemNamespace, CordonAndDrainMaintenanceFeatureFlag, maintenanceMode)

	// Trigger a rollout restart by adding/updating an annotation on the deployment
	return triggerNVCARollout(ctx, k8sClient, systemNamespace)
}

// addFeatureFlagToConfig adds a feature flag to the featureFlags array in the agent section
func addFeatureFlagToConfig(configYAML, featureFlag string) string {
	lines := strings.Split(configYAML, "\n")

	// Check if already has this feature flag
	for _, line := range lines {
		if strings.TrimLeft(line, " \t") == "- "+featureFlag {
			return configYAML // Already present
		}
	}

	// Find the featureFlags section and add the new flag
	for i, line := range lines {
		if strings.TrimLeft(line, " \t") == "featureFlags:" {
			// Found featureFlags, insert the new flag after this line
			lines = insertAfter(lines, i, "  - "+featureFlag)
			return strings.Join(lines, "\n")
		}
	}

	// No featureFlags section found, add it after "agent:" line
	for i, line := range lines {
		if line == "agent:" {
			// Insert featureFlags section after agent:
			lines = insertAfter(lines, i, "  - "+featureFlag)
			lines = insertAfter(lines, i, "  featureFlags:")
			return strings.Join(lines, "\n")
		}
	}

	return configYAML
}

// addMaintenanceModeToConfig adds maintenanceMode to the agent section of the config YAML
func addMaintenanceModeToConfig(configYAML, maintenanceMode string) string {
	lines := strings.Split(configYAML, "\n")

	// Check if maintenanceMode is already set and replace it
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "maintenanceMode:") {
			// Replace the line with new value, preserving indent
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "maintenanceMode: " + maintenanceMode
			return strings.Join(lines, "\n")
		}
	}

	// Insert maintenanceMode after "agent:" line
	for i, line := range lines {
		if line == "agent:" {
			lines = insertAfter(lines, i, "  maintenanceMode: "+maintenanceMode)
			break
		}
	}
	return strings.Join(lines, "\n")
}

func insertAfter(lines []string, index int, newLine string) []string {
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:index+1]...)
	result = append(result, newLine)
	result = append(result, lines[index+1:]...)
	return result
}

// triggerNVCARollout adds a restart annotation to the NVCA deployment to trigger a rollout
func triggerNVCARollout(ctx context.Context, k8sClient kubernetes.Interface, systemNamespace string) error {
	log := core.GetLogger(ctx)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := k8sClient.AppsV1().Deployments(systemNamespace).Get(ctx, NVCAModuleName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get NVCA deployment: %w", err)
		}

		// Add restart annotation to pod template to trigger rollout
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = make(map[string]string)
		}
		deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

		_, err = k8sClient.AppsV1().Deployments(systemNamespace).Update(ctx, deploy, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		log.Infof("Triggered NVCA deployment rollout in %s", systemNamespace)
		return nil
	})
}

// deleteICMSRequests removes finalizers from all ICMSRequest CRs and deletes them.
func deleteICMSRequests(ctx context.Context, dynamicClient dynamic.Interface, namespace string) error {
	log := core.GetLogger(ctx)

	icmsGVR := schema.GroupVersionResource{
		Group:    "nvca.nvcf.nvidia.io",
		Version:  "v2beta1",
		Resource: "icmsrequests",
	}

	list, err := dynamicClient.Resource(icmsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to list ICMS requests: %w", err)
	}

	if len(list.Items) == 0 {
		log.Debugf("No ICMS requests to delete in namespace %s", namespace)
		return nil
	}

	log.Infof("Deleting %d ICMS request(s) in namespace %s", len(list.Items), namespace)

	for _, item := range list.Items {
		name := item.GetName()

		// Remove all finalizers first with retry on conflict
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get the latest version
			latest, err := dynamicClient.Resource(icmsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return nil
				}
				return err
			}

			if len(latest.GetFinalizers()) == 0 {
				return nil
			}

			// Set the GVK - dynamic client Get doesn't always populate Kind, which is required for Update
			latest.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   icmsGVR.Group,
				Version: icmsGVR.Version,
				Kind:    "ICMSRequest",
			})
			latest.SetFinalizers(nil)
			_, err = dynamicClient.Resource(icmsGVR).Namespace(namespace).Update(ctx, latest, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			log.WithError(err).Warnf("failed to remove finalizers from ICMS request %s/%s", namespace, name)
			continue
		}
		log.Debugf("Removed finalizers from ICMS request %s/%s", namespace, name)

		// Delete the request
		err = dynamicClient.Resource(icmsGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to delete ICMS request %s/%s", namespace, name)
			continue
		}
		log.Debugf("Deleted ICMS request %s/%s", namespace, name)
	}

	return nil
}

// workloadNamespaceLabelSelector selects namespaces created by NVCA for workload instances.
const workloadNamespaceLabelSelector = "nvca.nvcf.nvidia.io/workload-instance-type"

// deleteWorkloadNamespaces lists and deletes all NVCA workload namespaces (sr-*).
func deleteWorkloadNamespaces(ctx context.Context, k8sClient kubernetes.Interface) error {
	log := core.GetLogger(ctx)

	nsList, err := k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: workloadNamespaceLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list workload namespaces: %w", err)
	}

	if len(nsList.Items) == 0 {
		log.Debug("No workload namespaces to delete")
		return nil
	}

	log.Infof("Deleting %d workload namespace(s)", len(nsList.Items))

	var errs []error
	for _, ns := range nsList.Items {
		if err := k8sClient.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
			if !k8serrors.IsNotFound(err) {
				log.WithError(err).Warnf("failed to delete workload namespace %s", ns.Name)
				errs = append(errs, err)
			}
		} else {
			log.Debugf("Deleted workload namespace %s", ns.Name)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to delete %d workload namespace(s)", len(errs))
	}
	return nil
}

// waitForDeploymentRollout waits for a deployment to complete its rollout
func waitForDeploymentRollout(ctx context.Context, k8sClient kubernetes.Interface, namespace, name string, timeout time.Duration) error {
	log := core.GetLogger(ctx)
	const pollInterval = 5 * time.Second

	start := time.Now()
	for time.Since(start) < timeout {
		deploy, err := k8sClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				log.Debugf("Deployment %s/%s not found, skipping rollout wait", namespace, name)
				return nil
			}
			return fmt.Errorf("failed to get deployment: %w", err)
		}

		// Check if rollout is complete:
		// - All replicas are updated
		// - All replicas are available
		// - No old replicas pending termination
		if deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.AvailableReplicas == *deploy.Spec.Replicas &&
			deploy.Status.UnavailableReplicas == 0 {
			return nil
		}

		log.Debugf("Waiting for deployment %s/%s rollout: updated=%d, available=%d, unavailable=%d, desired=%d",
			namespace, name,
			deploy.Status.UpdatedReplicas,
			deploy.Status.AvailableReplicas,
			deploy.Status.UnavailableReplicas,
			*deploy.Spec.Replicas)

		time.Sleep(pollInterval)
	}

	return fmt.Errorf("timeout waiting for deployment %s/%s rollout", namespace, name)
}

// Helper functions

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}
