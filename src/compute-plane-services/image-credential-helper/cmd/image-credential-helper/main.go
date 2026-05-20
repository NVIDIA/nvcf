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

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	var (
		globalMode                bool
		targetNamespace           string
		namespaceLabelSelectorStr string
		secretLabelSelectorStr    string
	)
	flag.BoolVar(&globalMode, "global", false, "Run for all namespaces matched by -target-namespace or -namespace-label-selector")
	flag.StringVar(&targetNamespace, "target-namespace", "", "Run for this namespace")
	flag.StringVar(&secretLabelSelectorStr, "secret-label-selector", "", "Run for all secrets matched by this selector")
	flag.StringVar(&namespaceLabelSelectorStr, "namespace-label-selector", "", "Run for all namespaces matched by this selector")
	flag.Parse()

	if namespaceLabelSelectorStr != "" {
		if _, err := labels.Parse(namespaceLabelSelectorStr); err != nil {
			logrus.WithError(err).Fatal("Invalid namespace label selector string")
		}
	}
	if secretLabelSelectorStr != "" {
		if _, err := labels.Parse(secretLabelSelectorStr); err != nil {
			logrus.WithError(err).Fatal("Invalid secret label selector string")
		}
	}

	ctx := context.Background()

	restConfig, err := config.GetConfig()
	if err != nil {
		logrus.WithError(err).Fatal("Failed to get REST config")
	}
	k8sClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to create kubernetes clientset")
	}

	if globalMode {
		err = runGlobal(ctx, k8sClient, targetNamespace, secretLabelSelectorStr, namespaceLabelSelectorStr)
	} else {
		err = runNamespaced(ctx, k8sClient, os.Getenv)
	}
	if err != nil {
		logrus.WithError(err).Fatal("Failed")
	}
}

var (
	imageCredsSecretNameRE = regexp.MustCompile(`^(.+)` + imagecredential.ImageCredsSecretNameSuffix + `$`)
	pullSecretSecretNameRE = regexp.MustCompile(`^(workload|worker)-(.+)-regcred-([0-9]+)$`)
)

// authConfigPair holds workload and worker registry auth configs for a single workload ID.
type authConfigPair struct {
	workloadAuthCfg common.RegistryAuthConfig
	workerAuthCfg   common.RegistryAuthConfig
}

// resolveNamespaces returns either the target namespace or namespaces matching the label selector.
func resolveNamespaces(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	targetNamespace, namespaceLabelSelectorStr string,
) ([]string, error) {
	if targetNamespace != "" {
		err := imagecredential.RetryK8s(func() error {
			_, err := k8sClient.CoreV1().Namespaces().Get(ctx, targetNamespace, metav1.GetOptions{})
			return err
		})
		if apierrors.IsNotFound(err) {
			logrus.WithField("targetNamespace", targetNamespace).Info("Target namespace not found")
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []string{targetNamespace}, nil
	}
	var namespaces []string
	err := imagecredential.RetryK8s(func() (err error) {
		namespaceList, err := k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
			LabelSelector: namespaceLabelSelectorStr,
		})
		if err == nil {
			for _, namespace := range namespaceList.Items {
				namespaces = append(namespaces, namespace.Name)
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return namespaces, nil
}

// listSecretsForNamespace lists secrets in the namespace matching the label selector.
func listSecretsForNamespace(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	namespace, secretLabelSelectorStr string,
) (*corev1.SecretList, error) {
	var secretList *corev1.SecretList
	err := imagecredential.RetryK8s(func() (err error) {
		secretList, err = k8sClient.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: secretLabelSelectorStr,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return secretList, nil
}

// buildAuthCfgsAndPullSecrets splits secrets into auth configs and pull secrets by workload ID.
func buildAuthCfgsAndPullSecrets(
	secrets []corev1.Secret,
	logger *logrus.Entry,
) (authCfgsByID map[string]authConfigPair, matchingPullSecretsByID map[string][]corev1.Secret, errs []error) {
	authCfgsByID = map[string]authConfigPair{}
	matchingPullSecretsByID = map[string][]corev1.Secret{}
	for _, secret := range secrets {
		if imageCredsMatches := imageCredsSecretNameRE.FindStringSubmatch(secret.Name); len(imageCredsMatches) == 2 && secret.Data != nil {
			workloadCfg, err := decodeRegistryAuthConfig(string(secret.Data[common.ContainerRegistriesCredentialsEnv]))
			if err != nil {
				logger.WithError(err).Error("Failed to decode container registry cred config")
				errs = append(errs, err)
				continue
			}
			workerAuthSecret, err := decodeRegistryAuthSecret(string(secret.Data[common.SidecarRegistryCredentialEnv]))
			if err != nil {
				logger.WithError(err).Error("Failed to decode sidecar registry cred config")
				errs = append(errs, err)
				continue
			}
			authCfgsByID[imageCredsMatches[1]] = authConfigPair{
				workloadAuthCfg: workloadCfg,
				workerAuthCfg:   common.RegistryAuthConfig{K8sSecrets: []common.RegistryAuthSecret{workerAuthSecret}},
			}
		} else if psMatches := pullSecretSecretNameRE.FindStringSubmatch(secret.Name); len(psMatches) == 4 &&
			secret.Type == corev1.SecretTypeDockerConfigJson {
			matchingPullSecretsByID[psMatches[2]] = append(matchingPullSecretsByID[psMatches[2]], secret)
		}
	}
	return authCfgsByID, matchingPullSecretsByID, errs
}

// updateSecretsForWorkloadIDs updates K8s pull secrets for each workload ID that has auth configs and matching secrets.
func updateSecretsForWorkloadIDs(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	authCfgsByID map[string]authConfigPair,
	matchingPullSecretsByID map[string][]corev1.Secret,
	namespace string,
	logger *logrus.Entry,
) (errs []error) {
	for wlIDComponent, authCfgs := range authCfgsByID {
		logger := logger.WithField("id", wlIDComponent)
		if len(authCfgs.workloadAuthCfg.K8sSecrets) == 0 && len(authCfgs.workerAuthCfg.K8sSecrets) == 0 {
			logger.Info("Skipping secret update for id without image creds secret")
			continue
		}
		matchingPullSecrets, ok := matchingPullSecretsByID[wlIDComponent]
		if !ok || len(matchingPullSecrets) == 0 {
			logger.Info("Skipping secret update for id without matching image pull secrets")
			continue
		}
		if err := imagecredential.UpdateMatchingSecrets(ctx,
			k8sClient,
			authCfgs.workloadAuthCfg, authCfgs.workerAuthCfg,
			matchingPullSecrets, namespace, wlIDComponent,
		); err != nil {
			logger.WithError(err).Error("K8s cred helper encountered errors")
			errs = append(errs, err)
		}
	}
	return errs
}

func runGlobal(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	targetNamespace,
	secretLabelSelectorStr, namespaceLabelSelectorStr string,
) (err error) {
	logrus.WithFields(logrus.Fields{
		"targetNamespace":        targetNamespace,
		"secretLabelSelector":    secretLabelSelectorStr,
		"namespaceLabelSelector": namespaceLabelSelectorStr,
	}).Info("Running with global config")

	namespaces, err := resolveNamespaces(ctx, k8sClient, targetNamespace, namespaceLabelSelectorStr)
	if err != nil {
		return err
	}
	if len(namespaces) == 0 {
		return nil
	}

	var errs []error
	for _, namespace := range namespaces {
		logger := logrus.WithField("namespace", namespace)
		secretList, listErr := listSecretsForNamespace(ctx, k8sClient, namespace, secretLabelSelectorStr)
		if listErr != nil {
			errs = append(errs, listErr)
			continue
		}
		authCfgsByID, matchingPullSecretsByID, parseErrs := buildAuthCfgsAndPullSecrets(secretList.Items, logger)
		errs = append(errs, parseErrs...)
		if len(authCfgsByID) == 0 {
			logger.Info("Skipping secret update for namespace without image creds secret")
			continue
		}
		if len(matchingPullSecretsByID) == 0 {
			logger.Info("Skipping secret update for namespace without matching image pull secrets")
			continue
		}
		updateErrs := updateSecretsForWorkloadIDs(ctx, k8sClient, authCfgsByID, matchingPullSecretsByID, namespace, logger)
		errs = append(errs, updateErrs...)
	}

	return errors.Join(errs...)
}

func runNamespaced(ctx context.Context, k8sClient kubernetes.Interface, getEnv func(string) string) error {
	logrus.Info("Running namespaced config")

	namespace := getEnv("NAMESPACE")
	if namespace == "" {
		return fmt.Errorf("env NAMESPACE not set")
	}
	wlIDComponent := getEnv("WORKLOAD_ID_COMPONENT")
	if wlIDComponent == "" {
		return fmt.Errorf("env WORKLOAD_ID_COMPONENT not set")
	}
	workloadImageCredsSet := getEnv(common.ContainerRegistriesCredentialsEnv)
	if workloadImageCredsSet == "" {
		return fmt.Errorf("env %s not set", common.ContainerRegistriesCredentialsEnv)
	}
	workloadAuthCfg, err := decodeRegistryAuthConfig(workloadImageCredsSet)
	if err != nil {
		logrus.WithError(err).Error("Failed to decode workload registry cred config")
		return err
	}
	workerImageCred := getEnv(common.SidecarRegistryCredentialEnv)
	if workerImageCred == "" {
		return fmt.Errorf("env %s not set", common.SidecarRegistryCredentialEnv)
	}
	workerAuthSecret, err := decodeRegistryAuthSecret(workerImageCred)
	if err != nil {
		logrus.WithError(err).Error("Failed to decode worker registry cred secret")
		return err
	}
	workerAuthCfg := common.RegistryAuthConfig{K8sSecrets: []common.RegistryAuthSecret{workerAuthSecret}}

	var secretList *corev1.SecretList
	if err = imagecredential.RetryK8s(func() (err error) {
		secretList, err = k8sClient.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		return err
	}); err != nil {
		return err
	}

	if err := imagecredential.UpdateMatchingSecrets(ctx, k8sClient,
		workloadAuthCfg, workerAuthCfg,
		secretList.Items, namespace, wlIDComponent,
	); err != nil {
		logrus.WithError(err).Error("K8s cred helper encountered errors")
		return err
	}

	return nil
}

func decodeRegistryAuthConfig(cred string) (cfg common.RegistryAuthConfig, err error) {
	if cred == "" {
		return cfg, nil
	}
	credBytes, err := base64.StdEncoding.DecodeString(cred)
	if err != nil {
		return common.RegistryAuthConfig{}, err
	}
	if err := json.Unmarshal(credBytes, &cfg); err != nil {
		return common.RegistryAuthConfig{}, err
	}
	return cfg, nil
}

func decodeRegistryAuthSecret(cred string) (secret common.RegistryAuthSecret, err error) {
	if cred == "" {
		return secret, nil
	}
	credBytes, err := base64.StdEncoding.DecodeString(cred)
	if err != nil {
		return common.RegistryAuthSecret{}, err
	}
	if err := json.Unmarshal(credBytes, &secret); err != nil {
		return common.RegistryAuthSecret{}, err
	}
	return secret, nil
}
