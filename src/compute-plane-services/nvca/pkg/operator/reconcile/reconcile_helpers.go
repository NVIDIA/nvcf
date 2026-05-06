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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const (
	restartedAtAnnotation = "nvca-operator.nvcf.nvidia.io/restartedAt"
	trueValue             = "true"
)

type RegistryConfig struct {
	Auths map[string]RegistryAuth `json:"auths"`
}

type RegistryAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Helper functions to check and remove string from a slice of strings.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func getImagePullSecretDockerConfigJSONFromNGCKey(registryServer, accessKey string) ([]byte, error) {
	rConf := RegistryConfig{
		Auths: map[string]RegistryAuth{
			registryServer: {
				Username: "$oauthtoken",
				Password: accessKey,
			},
		},
	}

	secretData, err := json.Marshal(rConf)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to get secretdata")
	}
	return secretData, nil
}

// decodeEnvOverrides decodes a base64-encoded JSON map of environment variable overrides
func decodeEnvOverrides(b64 string) (map[string]string, error) {
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	var envOverrides map[string]string
	if err := json.Unmarshal(data, &envOverrides); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return envOverrides, nil
}

func getAppLabels() map[string]string {
	return map[string]string{
		InstanceLabelKey:  nvcaoptypes.NVCAModuleName,
		ManagedbyLabelKey: NVCAOperatorName,
		NameLabelKey:      nvcaoptypes.NVCAModuleName,
	}
}

func getNBAnnotations(nb *nvidiaiov1.NVCFBackend) map[string]string {
	return map[string]string{
		ClusterName:     nb.Spec.ClusterConfig.ClusterName,
		ClusterGroupKey: nb.Spec.ClusterConfig.ClusterGroupName,
	}
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateNamespace(ctx context.Context, ns *v1.Namespace) error {
	// get and create if not exists
	_, err := bc.clients.K8s.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create %v namespace, err: %v", ns.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v namespace, err: %v", ns.Name, err)
		}
	} else {
		// update namespace with new labels
		_, err = bc.clients.K8s.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v namespace, err: %v", ns.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateResourceQuota(ctx context.Context, rq *v1.ResourceQuota) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.CoreV1().ResourceQuotas(rq.Namespace).Get(ctx, rq.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().ResourceQuotas(rq.Namespace).Create(ctx, rq, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v resourcequota, err: %v", rq.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v resourcequota, err: %v", rq.Name, err)
		}
	} else {
		// update resourcequota
		_, err = bc.clients.K8s.CoreV1().ResourceQuotas(rq.Namespace).Update(ctx, rq, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v resourcequota, err: %v", rq.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateConfigMap(ctx context.Context, cm *v1.ConfigMap) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v configMap, err: %v", cm.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v configMap, err: %v", cm.Name, err)
		}
	} else {
		// update configmap
		_, err = bc.clients.K8s.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v configMap, err: %v", cm.Name, err)
		}
	}
	return nil
}

func (bc *BackendK8sCache) deleteConfigMapIfExists(ctx context.Context, namespace, name string) error {
	err := bc.clients.K8s.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete configMap %v/%v, err: %w", namespace, name, err)
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateSecret(ctx context.Context, s *v1.Secret) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().Secrets(s.Namespace).Create(ctx, s, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v secret, err: %v", s.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v secret, err: %v", s.Name, err)
		}
	} else {
		// update secret
		_, err = bc.clients.K8s.CoreV1().Secrets(s.Namespace).Update(ctx, s, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v secret, err: %v", s.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateDeployment(ctx context.Context, d *appsv1.Deployment) error {
	log := core.GetLogger(ctx)

	// get and create if not exists, updated if it does
	existingDep, err := bc.clients.K8s.AppsV1().Deployments(d.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.AppsV1().Deployments(d.Namespace).Create(ctx, d, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v deployment, err: %v", d.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v deployment, err: %v", d.Name, err)
		}
	} else if !objectsEqual(existingDep.Spec.Selector, d.Spec.Selector) {
		// Delete and recreate the deployment if the MatchLabels changed
		log.Infof("deleting deployment %v because matchLabels changed", d.Name)
		// Execute the delete in the foreground with a timeout (default 60s)
		// this deployment has few finalizers so it should be quick
		// Inspired by
		// https://github.com/kubernetes/client-go/blob/4ebe42d8c9c18f464fcc7b4f15b3a632db4cbdb2/examples/create-update-delete-deployment/main.go#L154-L163
		timeoutCtx, cancel := context.WithTimeout(ctx, bc.deploymentDeleteTimeout)
		defer cancel()
		deletePolicy := metav1.DeletePropagationForeground
		err := bc.clients.K8s.AppsV1().Deployments(d.Namespace).Delete(timeoutCtx, d.Name, metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		})
		if err != nil {
			return fmt.Errorf("failed to delete %v deployment, err: %v", d.Name, err)
		}
		_, err = bc.clients.K8s.AppsV1().Deployments(d.Namespace).Create(ctx, d, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create %v deployment, err: %v", d.Name, err)
		}
	} else {
		// Rollout restart to pick up configmap and secret changes.
		// https://github.com/kubernetes-client/python/issues/1378#issuecomment-779323573
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = map[string]string{}
		}
		d.Spec.Template.Annotations[restartedAtAnnotation] = time.Now().Format(time.RFC3339Nano)

		// update deployment
		_, err = bc.clients.K8s.AppsV1().Deployments(d.Namespace).Update(ctx, d, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v deployment, err: %v", d.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateService(ctx context.Context, s *v1.Service) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.CoreV1().Services(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().Services(s.Namespace).Create(ctx, s, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v service, err: %v", s.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v service, err: %v", s.Name, err)
		}
	} else {
		// update service
		_, err = bc.clients.K8s.CoreV1().Services(s.Namespace).Update(ctx, s, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v service, err: %v", s.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateClusterRole(ctx context.Context, r *rbacv1.ClusterRole) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.RbacV1().ClusterRoles().Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.RbacV1().ClusterRoles().Create(ctx, r, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v clusterRole, err: %v", r.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v clusterRole, err: %v", r.Name, err)
		}
	} else {
		// update clusterRole
		_, err = bc.clients.K8s.RbacV1().ClusterRoles().Update(ctx, r, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v clusterRole, err: %v", r.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateClusterRoleBinding(ctx context.Context, crb *rbacv1.ClusterRoleBinding) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.RbacV1().ClusterRoleBindings().Get(ctx, crb.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v clusterRoleBinding, err: %v", crb.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v clusterRole, err: %v", crb.Name, err)
		}
	} else {
		// update clusterRoleBinding
		_, err = bc.clients.K8s.RbacV1().ClusterRoleBindings().Update(ctx, crb, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v clusterRoleBinding, err: %v", crb.Name, err)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateServiceAccount(ctx context.Context, sa *v1.ServiceAccount) error {
	// get and create if not exists, updated if it does
	_, err := bc.clients.K8s.CoreV1().ServiceAccounts(sa.Namespace).Get(ctx, sa.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.CoreV1().ServiceAccounts(sa.Namespace).Create(ctx, sa, metav1.CreateOptions{})
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create %v serviceAccount, err: %v", sa.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v serviceAccount, err: %v", sa.Name, err)
		}
	} else {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			saLatest, err := bc.clients.K8s.CoreV1().ServiceAccounts(sa.Namespace).Update(ctx, sa, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to get latest version of serviceAccount: %v", err)
			}

			// Update the AutomountServiceAccountToken field directly on the latest ServiceAccount object
			saLatest.AutomountServiceAccountToken = sa.AutomountServiceAccountToken

			_, updateErr := bc.clients.K8s.CoreV1().ServiceAccounts(sa.Namespace).Update(ctx, saLatest, metav1.UpdateOptions{})
			return updateErr
		})

		if retryErr != nil {
			return fmt.Errorf("failed to update %v serviceAccount, err: %v", sa.Name, retryErr)
		}
	}
	return nil
}

//nolint:dupl
func (bc *BackendK8sCache) createOrUpdateValidatingWebhookConfiguration(ctx context.Context,
	vw *admissionregistrationv1.ValidatingWebhookConfiguration) error {
	// get and create if not exists, updated if it does
	cw, err := bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, vw.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, vw, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create %v validatingWebhookConfiguration, err: %v", vw.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get %v validatingWebhookConfiguration, err: %v", vw.Name, err)
		}
	} else {
		// update validating webhook
		vw.ObjectMeta.UID = cw.ObjectMeta.UID                         //nolint:staticcheck
		vw.ObjectMeta.ResourceVersion = cw.ObjectMeta.ResourceVersion //nolint:staticcheck
		_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(ctx, vw, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update %v validatingWebhookConfiguration, err: %v", vw.Name, err)
		}
	}
	return nil
}

func (bc *BackendK8sCache) createOrUpdateWebhookConfiguration(ctx context.Context, name string, isMutating bool, webhook interface{}) error {
	if isMutating {
		vw := webhook.(*admissionregistrationv1.MutatingWebhookConfiguration)
		// get and create if not exists, updated if it does
		cw, err := bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				_, err := bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, vw, metav1.CreateOptions{})
				if err != nil {
					return fmt.Errorf("failed to create %v MutatingWebhookConfiguration, err: %v", name, err)
				}
			} else {
				return fmt.Errorf("failed to get %v MutatingWebhookConfiguration, err: %v", name, err)
			}
		} else {
			// update Mutating webhook
			vw.ObjectMeta.UID = cw.ObjectMeta.UID                         //nolint:staticcheck
			vw.ObjectMeta.ResourceVersion = cw.ObjectMeta.ResourceVersion //nolint:staticcheck
			_, err = bc.clients.K8s.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(ctx, vw, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update %v MutatingWebhookConfiguration, err: %v", name, err)
			}
		}
	} else {
		vw := webhook.(*admissionregistrationv1.ValidatingWebhookConfiguration)
		// get and create if not exists, updated if it does
		cw, err := bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				_, err := bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, vw, metav1.CreateOptions{})
				if err != nil {
					return fmt.Errorf("failed to create %v ValidatingWebhookConfiguration, err: %v", name, err)
				}
			} else {
				return fmt.Errorf("failed to get %v ValidatingWebhookConfiguration, err: %v", name, err)
			}
		} else {
			// update Validating webhook
			vw.ObjectMeta.UID = cw.ObjectMeta.UID                         //nolint:staticcheck
			vw.ObjectMeta.ResourceVersion = cw.ObjectMeta.ResourceVersion //nolint:staticcheck
			_, err = bc.clients.K8s.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(ctx, vw, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update %v ValidatingWebhookConfiguration, err: %v", name, err)
			}
		}
	}
	return nil
}

func (bc *BackendK8sCache) createOrUpdateMutatingWebhookConfiguration(ctx context.Context,
	vw *admissionregistrationv1.MutatingWebhookConfiguration) error {
	return bc.createOrUpdateWebhookConfiguration(ctx, vw.Name, true, vw)
}

//nolint:unused // Used only in tests - do not use in production code
func (bc *BackendK8sCache) getCustomAnnotationsData(ctx context.Context,
	configMapGetter func(ctx context.Context, name string, opts metav1.GetOptions) (*v1.ConfigMap, error)) (map[string]string, error) {
	log := core.GetLogger(ctx)

	// Try to get the custom annotations configmap
	cm, err := configMapGetter(ctx, nvcfCustomAnnotationsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			log.Info("custom annotations configmap not found, returning empty map")
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to get custom annotations configmap: %w", err)
	}

	// Parse the annotations.json data
	if annotationsJSON, ok := cm.Data["annotations.json"]; ok {
		var annotations map[string]string
		if err := json.Unmarshal([]byte(annotationsJSON), &annotations); err != nil {
			return nil, fmt.Errorf("failed to parse annotations.json: %w", err)
		}
		return annotations, nil
	}

	// Return empty map if no annotations.json key found
	return map[string]string{}, nil
}
