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
	"fmt"
	"net/url"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	credhelper "github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
)

func Test_runGlobal(t *testing.T) {
	const namespaceName = "sr-4e4aaa7e-eb15-42b7-9bd3-fab7e8fd71ae"
	const otherNamespaceName1 = "other-namespace-1"
	const otherNamespaceName2 = "other-namespace-2"
	const wlIDComponent = namespaceName
	secretSelectorLabels := labels.Set{
		"foo": "bar",
	}
	secretSelectorLabelStr := secretSelectorLabels.AsSelector().String()
	namespaceSelectorLabels := labels.Set{
		"my": "namespace",
	}
	namespaceSelectorLabelStr := namespaceSelectorLabels.AsSelector().String()

	authHelper := fakeAuthHelper{
		matchers: []string{
			"foobar.com",
		},
		public: []bool{
			false,
		},
		creds: []map[string]map[string]struct {
			username string
			password string
		}{
			{
				"basicuser_foobar": map[string]struct {
					username string
					password string
				}{
					"basicpassword_foobar": {
						username: "authuser_new_foobar",
						password: "authtoken_new_foobar",
					},
				},
				"basicuser_foobar2": map[string]struct {
					username string
					password string
				}{
					"basicpassword_foobar2": {
						username: "authuser_new_foobar2",
						password: "authtoken_new_foobar2",
					},
				},
			},
		},
	}

	credhelper.RegisterAuthHelper("fake", authHelper)

	ctx := context.Background()

	// Should update workload-...-regcred-1 and worker-...-regcred-0
	workloadCredCfg := common.RegistryAuthConfig{
		K8sSecrets: []common.RegistryAuthSecret{
			{
				Auths: map[string]common.RegistryAuth{
					"somereg.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("static_username1:static_password1")),
					},
				},
			},
			{
				Auths: map[string]common.RegistryAuth{
					"somereg.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2")),
					},
					"foobar.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar:basicpassword_foobar")),
					},
				},
			},
		},
	}
	workloadCredCfgData, err := json.Marshal(workloadCredCfg)
	require.NoError(t, err)
	workerCredSecret := common.RegistryAuthSecret{
		Auths: map[string]common.RegistryAuth{
			"foobar.com": {
				Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar2:basicpassword_foobar2")),
			},
		},
	}
	workerCredSecretData, err := json.Marshal(workerCredSecret)
	require.NoError(t, err)
	imageCredsSecret1, err := imagecredential.NewImageCredsSecret(wlIDComponent, map[string]string{
		common.ContainerRegistriesCredentialsEnv: base64.StdEncoding.EncodeToString(workloadCredCfgData),
		common.SidecarRegistryCredentialEnv:      base64.StdEncoding.EncodeToString(workerCredSecretData),
	})
	require.NoError(t, err)
	imageCredsSecret1.Namespace = namespaceName
	imageCredsSecret1.Labels = secretSelectorLabels
	imageCredsSecret2 := imageCredsSecret1.DeepCopy()
	imageCredsSecret2.Namespace = otherNamespaceName1
	imageCredsSecret3 := imageCredsSecret1.DeepCopy()
	imageCredsSecret3.Namespace = otherNamespaceName2
	require.NoError(t, err)

	require.NoError(t, err)
	workloadSecretNoNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + wlIDComponent + "-regcred-0",
			Namespace: namespaceName,
			Labels:    secretSelectorLabels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username1:static_password1")) + `"}}}`),
		},
	}
	workloadSecretNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + wlIDComponent + "-regcred-1",
			Namespace: namespaceName,
			Labels:    secretSelectorLabels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2")) + `"},` +
					`"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar:authtoken_old_foobar")) + `"}}}`,
			),
		},
	}
	workerSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-" + wlIDComponent + "-regcred-0",
			Namespace: namespaceName,
			Labels:    secretSelectorLabels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar2:authtoken_old_foobar2")) + `"}}}`,
			),
		},
	}
	otherSecret1 := workloadSecretNeedsUpdate.DeepCopy()
	otherSecret1.Namespace = otherNamespaceName1
	otherSecret2 := workloadSecretNeedsUpdate.DeepCopy()
	otherSecret2.Namespace = otherNamespaceName2
	k8sClient := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName, Labels: namespaceSelectorLabels}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName1, Labels: namespaceSelectorLabels}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName2, Labels: map[string]string{"my": "othernamespace"}}},
		imageCredsSecret1.DeepCopy(), imageCredsSecret2.DeepCopy(), imageCredsSecret3.DeepCopy(),
		workloadSecretNoNeedsUpdate.DeepCopy(), workloadSecretNeedsUpdate.DeepCopy(),
		workerSecret.DeepCopy(), otherSecret1.DeepCopy(), otherSecret2.DeepCopy(),
	)

	// First test missing worker creds env.
	delete(imageCredsSecret1.Data, common.SidecarRegistryCredentialEnv)
	_, err = k8sClient.CoreV1().Secrets(imageCredsSecret1.Namespace).Update(ctx, imageCredsSecret1, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = runGlobal(ctx, k8sClient, namespaceName, secretSelectorLabelStr, "")
	require.NoError(t, err)

	expWorkloadUpdate := `{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2")) + `"},` +
		`"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_new_foobar:authtoken_new_foobar")) + `"}}}`

	gotSecretNeedsUpdate, err := k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkloadUpdate, string(gotSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]))

	gotWorkerSecret, err := k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workerSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workerSecret.Data, gotWorkerSecret.Data)

	// Then add the env back and test expected flow.
	expWorkerUpdate := `{"auths":{"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_new_foobar2:authtoken_new_foobar2")) + `"}}}`

	imageCredsSecret1.Data[common.SidecarRegistryCredentialEnv] = []byte(base64.StdEncoding.EncodeToString(workerCredSecretData))
	_, err = k8sClient.CoreV1().Secrets(imageCredsSecret1.Namespace).Update(ctx, imageCredsSecret1, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = runGlobal(ctx, k8sClient, namespaceName, secretSelectorLabelStr, "")
	require.NoError(t, err)

	gotSecretNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkloadUpdate, string(gotSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]))
	gotWorkerSecret, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workerSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkerUpdate, string(gotWorkerSecret.Data[corev1.DockerConfigJsonKey]))

	gotSecretNoNeedsUpdate, err := k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNoNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workloadSecretNoNeedsUpdate.Data, gotSecretNoNeedsUpdate.Data)

	gotOtherSecret1, err := k8sClient.CoreV1().Secrets(otherNamespaceName1).Get(ctx, otherSecret1.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret1.Data, gotOtherSecret1.Data)

	gotOtherSecret2, err := k8sClient.CoreV1().Secrets(otherNamespaceName2).Get(ctx, otherSecret2.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret1.Data, gotOtherSecret2.Data)

	// With namespace selector, should see two secrets in different namespaces updated.
	err = runGlobal(ctx, k8sClient, "", "", namespaceSelectorLabelStr)
	require.NoError(t, err)

	gotSecretNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkloadUpdate, string(gotSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]))
	gotOtherSecret1, err = k8sClient.CoreV1().Secrets(otherNamespaceName1).Get(ctx, otherSecret1.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkloadUpdate, string(gotOtherSecret1.Data[corev1.DockerConfigJsonKey]))

	gotSecretNoNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNoNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workloadSecretNoNeedsUpdate.Data, gotSecretNoNeedsUpdate.Data)
	gotWorkerSecret, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workerSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expWorkerUpdate, string(gotWorkerSecret.Data[corev1.DockerConfigJsonKey]))

	gotOtherSecret2, err = k8sClient.CoreV1().Secrets(otherNamespaceName2).Get(ctx, otherSecret2.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret2.Data, gotOtherSecret2.Data)

	// Test not found
	workloadCredCfg = common.RegistryAuthConfig{
		K8sSecrets: []common.RegistryAuthSecret{
			{
				Auths: map[string]common.RegistryAuth{
					"foobar.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar:notfound")),
					},
				},
			},
		},
	}
	workloadCredCfgData, err = json.Marshal(workloadCredCfg)
	require.NoError(t, err)
	workerCredSecret = common.RegistryAuthSecret{
		Auths: map[string]common.RegistryAuth{
			"foobar.com": {
				Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar2:notfound")),
			},
		},
	}
	workerCredSecretData, err = json.Marshal(workerCredSecret)
	require.NoError(t, err)
	imageCredsSecret1, err = imagecredential.NewImageCredsSecret(wlIDComponent, map[string]string{
		common.ContainerRegistriesCredentialsEnv: base64.StdEncoding.EncodeToString(workloadCredCfgData),
		common.SidecarRegistryCredentialEnv:      base64.StdEncoding.EncodeToString(workerCredSecretData),
	})
	require.NoError(t, err)
	imageCredsSecret1.Namespace = namespaceName
	imageCredsSecret1.Labels = secretSelectorLabels
	imageCredsSecret2 = imageCredsSecret1.DeepCopy()
	imageCredsSecret2.Namespace = otherNamespaceName1
	imageCredsSecret3 = imageCredsSecret1.DeepCopy()
	imageCredsSecret3.Namespace = otherNamespaceName2
	require.NoError(t, err)

	k8sClient = k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName, Labels: namespaceSelectorLabels}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName1, Labels: namespaceSelectorLabels}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName2, Labels: map[string]string{"my": "othernamespace"}}},
		imageCredsSecret1.DeepCopy(), imageCredsSecret2.DeepCopy(), imageCredsSecret3.DeepCopy(),
		workloadSecretNoNeedsUpdate.DeepCopy(), workloadSecretNeedsUpdate.DeepCopy(),
		workerSecret.DeepCopy(), otherSecret1.DeepCopy(), otherSecret2.DeepCopy(),
	)

	expOld := `{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2")) + `"},` +
		`"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar:authtoken_old_foobar")) + `"}}}`

	err = runGlobal(ctx, k8sClient, namespaceName, secretSelectorLabelStr, "")
	require.NoError(t, err)

	gotSecretNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expOld, string(gotSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]))
	gotWorkerSecret, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workerSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workerSecret.Data, gotWorkerSecret.Data)

	gotSecretNoNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNoNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workloadSecretNoNeedsUpdate.Data, gotSecretNoNeedsUpdate.Data)

	gotOtherSecret1, err = k8sClient.CoreV1().Secrets(otherNamespaceName1).Get(ctx, otherSecret1.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret1.Data, gotOtherSecret1.Data)

	gotOtherSecret2, err = k8sClient.CoreV1().Secrets(otherNamespaceName2).Get(ctx, otherSecret2.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret1.Data, gotOtherSecret2.Data)

	// With namespace selector, should see two secrets in different namespaces not updated.
	err = runGlobal(ctx, k8sClient, "", "", namespaceSelectorLabelStr)
	require.NoError(t, err)

	gotSecretNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expOld, string(gotSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]))
	gotOtherSecret1, err = k8sClient.CoreV1().Secrets(otherNamespaceName1).Get(ctx, otherSecret1.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t, expOld, string(gotOtherSecret1.Data[corev1.DockerConfigJsonKey]))

	gotSecretNoNeedsUpdate, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workloadSecretNoNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workloadSecretNoNeedsUpdate.Data, gotSecretNoNeedsUpdate.Data)
	gotWorkerSecret, err = k8sClient.CoreV1().Secrets(namespaceName).Get(ctx, workerSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workerSecret.Data, gotWorkerSecret.Data)

	gotOtherSecret2, err = k8sClient.CoreV1().Secrets(otherNamespaceName2).Get(ctx, otherSecret2.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret2.Data, gotOtherSecret2.Data)
}

type fakeAuthHelper struct {
	matchers []string
	public   []bool
	creds    []map[string]map[string]struct {
		username, password string
	}
}

func (h fakeAuthHelper) Matches(serverURL *url.URL) (match bool, isPublic bool) {
	hostname := serverURL.Hostname()
	for i, m := range h.matchers {
		if m == hostname {
			return true, h.public[i]
		}
	}
	return false, false
}

func (h fakeAuthHelper) Run(ctx context.Context, refURL *url.URL, creds credhelper.AuthHelperCredentials) (username, password string, err error) {
	idx := -1
	hostname := refURL.Hostname()
	for i, m := range h.matchers {
		if m == hostname {
			idx = i
			break
		}
	}
	if idx == -1 {
		err = fmt.Errorf("no match")
		return
	}
	cred := h.creds[idx]
	tokens, ok := cred[creds.Username]
	if !ok {
		err = fmt.Errorf("unknown key ID %s", creds.Username)
		return
	}
	v, ok := tokens[creds.Password]
	if !ok {
		err = fmt.Errorf("unknown secret key %s", creds.Password)
		return
	}
	return v.username, v.password, nil
}
