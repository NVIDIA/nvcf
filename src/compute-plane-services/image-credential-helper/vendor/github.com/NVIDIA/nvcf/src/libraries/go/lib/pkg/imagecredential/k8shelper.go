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

package imagecredential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UpdateMatchingSecrets updates existing NVCF workload and worker pull secrets with short-lived credentials.
func UpdateMatchingSecrets(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	workloadCredCfg, workerCredCfg common.RegistryAuthConfig,
	secrets []corev1.Secret,
	namespace, wlIDComponent string,
) error {
	return updateMatchingSecrets(ctx, k8sClient, getCustomAuthHelpers(), workloadCredCfg, workerCredCfg, secrets, namespace, wlIDComponent)
}

func updateMatchingSecrets(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	customAuthHelpers []CustomAuthHelper,
	workloadCredCfg, workerCredCfg common.RegistryAuthConfig,
	secrets []corev1.Secret,
	namespace, wlIDComponent string,
) (err error) {
	log := logrus.WithFields(logrus.Fields{
		"namespace": namespace,
		"id":        wlIDComponent,
	})
	secretClient := k8sClient.CoreV1().Secrets(namespace)
	secretSet := buildDockerSecretSet(secrets)
	credCache := map[string]map[string]credCacheItem{}
	updateSecret := newUpdateSecretCachedFunc(credCache)

	var errs []error
	log.Info("Updating workload secrets")
	namePrefix := fmt.Sprintf("workload-%s-regcred", wlIDComponent)
	errs = append(errs, updateSecretsForCredCfg(ctx, secretClient, secretSet, updateSecret, customAuthHelpers, workloadCredCfg, namePrefix, log)...)
	log.Info("Updating worker secrets")
	namePrefix = fmt.Sprintf("worker-%s-regcred", wlIDComponent)
	errs = append(errs, updateSecretsForCredCfg(ctx, secretClient, secretSet, updateSecret, customAuthHelpers, workerCredCfg, namePrefix, log)...)

	return errors.Join(errs...)
}

func buildDockerSecretSet(secrets []corev1.Secret) map[string]struct{} {
	secretSet := map[string]struct{}{}
	for _, secret := range secrets {
		if secret.Type == corev1.SecretTypeDockerConfigJson {
			secretSet[secret.Name] = struct{}{}
		}
	}
	return secretSet
}

type updateSecretFunc func(ctx context.Context, log *logrus.Entry, customAuthHelpers []CustomAuthHelper, secret common.RegistryAuthSecret) bool

func updateSecretsForCredCfg(
	ctx context.Context,
	secretClient interface {
		Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
		Update(ctx context.Context, secret *corev1.Secret, opts metav1.UpdateOptions) (*corev1.Secret, error)
	},
	secretSet map[string]struct{},
	updateSecret updateSecretFunc,
	customAuthHelpers []CustomAuthHelper,
	credCfg common.RegistryAuthConfig,
	namePrefix string,
	log *logrus.Entry,
) (errs []error) {
	for i, secret := range credCfg.K8sSecrets {
		log := log.WithField("cred_index", i)
		if !updateSecret(ctx, log, customAuthHelpers, secret) {
			continue
		}
		b, err := json.Marshal(secret)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		name := fmt.Sprintf("%s-%d", namePrefix, i)
		if _, ok := secretSet[name]; !ok {
			log.WithField("name", name).Info("Secret not present in namespace, skipping update")
			continue
		}
		if err := applySecretUpdate(ctx, secretClient, name, b, log); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func applySecretUpdate(
	ctx context.Context,
	secretClient interface {
		Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
		Update(ctx context.Context, secret *corev1.Secret, opts metav1.UpdateOptions) (*corev1.Secret, error)
	},
	name string,
	b []byte,
	log *logrus.Entry,
) error {
	return RetryK8s(func() error {
		secret, err := secretClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			log.WithField("name", name).Info("Secret not found, skipping update")
			return nil
		}
		secret.Data = map[string][]byte{
			corev1.DockerConfigJsonKey: b,
		}
		log.WithField("name", name).Info("Updating secret")
		if _, err := secretClient.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			log.WithField("name", name).Info("Secret not found, skipping update")
		}
		return nil
	})
}

type credCacheItem struct {
	username    string
	token       string
	needsUpdate bool
}

func newUpdateSecretCachedFunc(credCache map[string]map[string]credCacheItem) updateSecretFunc {
	return func(
		ctx context.Context,
		log *logrus.Entry,
		customAuthHelpers []CustomAuthHelper,
		secret common.RegistryAuthSecret,
	) bool {
		return updateSecretAuths(ctx, log, customAuthHelpers, credCache, secret)
	}
}

func updateSecretAuths(
	ctx context.Context,
	log *logrus.Entry,
	customAuthHelpers []CustomAuthHelper,
	credCache map[string]map[string]credCacheItem,
	secret common.RegistryAuthSecret,
) (anyUpdated bool) {
	for regHost, auth := range secret.Auths {
		log := log.WithFields(logrus.Fields{"host": regHost})
		username, token, needsUpdate, err := getOrFetchCachedCreds(ctx, credCache, customAuthHelpers, regHost, auth, log)
		if err != nil {
			log.WithError(err).Error("Failed to get auth update")
			continue
		}
		if needsUpdate {
			anyUpdated = true
			secret.Auths[regHost] = common.RegistryAuth{
				Auth: base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", username, token)),
			}
		}
	}
	return anyUpdated
}

func getOrFetchCachedCreds(
	ctx context.Context,
	credCache map[string]map[string]credCacheItem,
	customAuthHelpers []CustomAuthHelper,
	regHost string,
	auth common.RegistryAuth,
	log *logrus.Entry,
) (username, token string, needsUpdate bool, err error) {
	regHostCache, ok := credCache[regHost]
	if !ok {
		regHostCache = map[string]credCacheItem{}
		credCache[regHost] = regHostCache
	}
	if cacheVal, ok := regHostCache[auth.Auth]; ok {
		log.Info("Using cached username and token for auth")
		return cacheVal.username, cacheVal.token, cacheVal.needsUpdate, nil
	}
	log.Info("Fetching username and token for auth")
	username, token, needsUpdate, err = getAuthUpdate(ctx, customAuthHelpers, regHost, auth)
	if err != nil {
		return "", "", false, err
	}
	regHostCache[auth.Auth] = credCacheItem{needsUpdate: needsUpdate, username: username, token: token}
	return username, token, needsUpdate, nil
}

// RetryK8s retries retryable Kubernetes API errors with the client-go default backoff.
func RetryK8s(fn func() error) error {
	return retry.OnError(retry.DefaultBackoff, isRetryableK8sError, fn)
}

func isRetryableK8sError(err error) bool {
	reason, httpCode := reasonAndCodeForError(err)
	switch httpCode {
	case http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	if httpCode >= 500 {
		return true
	}
	if reason == metav1.StatusReasonExpired {
		return true
	}
	return false
}

func reasonAndCodeForError(err error) (metav1.StatusReason, int32) {
	if status, ok := err.(apierrors.APIStatus); ok || errors.As(err, &status) {
		return status.Status().Reason, status.Status().Code
	}
	return metav1.StatusReasonUnknown, 0
}

func getAuthUpdate(
	ctx context.Context,
	customAuthHelpers []CustomAuthHelper,
	ref string,
	auth common.RegistryAuth,
) (username, token string, needsUpdate bool, err error) {
	basicUsername, basicPassword, err := parseAuth(auth)
	if err != nil {
		return "", "", false, err
	}

	refURL, err := parseRef(ref)
	if err != nil {
		return "", "", false, err
	}

	for _, authHelper := range customAuthHelpers {
		if match, _ := authHelper.Matches(refURL); match {
			creds := AuthHelperCredentials{Username: basicUsername, Password: basicPassword}
			username, token, err = authHelper.Run(ctx, refURL, creds)
			needsUpdate = true
			return
		}
	}

	return basicUsername, basicPassword, false, nil
}

func parseAuth(auth common.RegistryAuth) (username, password string, err error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(auth.Auth)
	if err != nil {
		return "", "", fmt.Errorf("decode helm registry secret: %v", err)
	}
	split := bytes.SplitN(decodedBytes, []byte{':'}, 2)
	if len(split) != 2 {
		return "", "", fmt.Errorf("helm registry secret does not contain ':'")
	}
	username = string(bytes.TrimSpace(split[0]))
	password = string(bytes.TrimSpace(split[1]))
	return
}
