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

package credhelper

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"net/url"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestUpdateMatchingSecrets(t *testing.T) {
	const namespaceName = "sr-4e4aaa7e-eb15-42b7-9bd3-fab7e8fd71ae"
	const wlIDComponent = namespaceName
	workloadSecretNoNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + namespaceName + "-regcred-0",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username1:static_password1")) + `"}}}`),
		},
	}
	workloadSecretNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + namespaceName + "-regcred-1",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2")) + `"},` +
					`"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar:authtoken_old_foobar")) + `"}}}`,
			),
		},
	}
	workloadSecretNeedsUpdateCached := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + namespaceName + "-regcred-2",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar:authtoken_old_foobar")) + `"}}}`,
			),
		},
	}
	workerSecretNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-" + namespaceName + "-regcred-0",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar2:authtoken_old_foobar2")) + `"}}}`,
			),
		},
	}
	otherSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myworker-" + namespaceName + "-regcred-0",
			Namespace: namespaceName,
		},
		Data: map[string][]byte{
			"foo": []byte("bar"),
		},
	}
	k8sClient := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
		workloadSecretNoNeedsUpdate.DeepCopy(), workloadSecretNeedsUpdate.DeepCopy(),
		workloadSecretNeedsUpdateCached.DeepCopy(), workerSecretNeedsUpdate.DeepCopy(),
		otherSecret.DeepCopy(),
	)

	authHelpers := []CustomAuthHelper{
		fakeAuthHelper{
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
		},
	}

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
			{
				Auths: map[string]common.RegistryAuth{
					"foobar.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar:basicpassword_foobar")),
					},
				},
			},
		},
	}
	workerCredCfg := common.RegistryAuthConfig{
		K8sSecrets: []common.RegistryAuthSecret{
			{
				Auths: map[string]common.RegistryAuth{
					"foobar.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("basicuser_foobar2:basicpassword_foobar2")),
					},
				},
			},
		},
	}

	ctx := context.Background()
	secretClient := k8sClient.CoreV1().Secrets(namespaceName)

	secretList, err := secretClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	err = updateMatchingSecrets(ctx, k8sClient, authHelpers, workloadCredCfg, workerCredCfg, secretList.Items, wlIDComponent, namespaceName)
	require.NoError(t, err)

	gotWorkloadSecretNeedsUpdate, err := secretClient.Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"somereg.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2"))+`"},`+
			`"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("authuser_new_foobar:authtoken_new_foobar"))+`"}}}`,
		string(gotWorkloadSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]),
	)
	gotWorkloadSecretNeedsUpdateCached, err := secretClient.Get(ctx, workloadSecretNeedsUpdateCached.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("authuser_new_foobar:authtoken_new_foobar"))+`"}}}`,
		string(gotWorkloadSecretNeedsUpdateCached.Data[corev1.DockerConfigJsonKey]),
	)
	gotWorkerSecretNeedsUpdate, err := secretClient.Get(ctx, workerSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("authuser_new_foobar2:authtoken_new_foobar2"))+`"}}}`,
		string(gotWorkerSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]),
	)

	gotSecretNoNeedsUpdate, err := secretClient.Get(ctx, workloadSecretNoNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, workloadSecretNoNeedsUpdate.Data, gotSecretNoNeedsUpdate.Data)
	gotOtherSecret, err := secretClient.Get(ctx, otherSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret.Data, gotOtherSecret.Data)

	// Test not found
	k8sClient = k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
		workloadSecretNeedsUpdate.DeepCopy(),
	)
	secretClient = k8sClient.CoreV1().Secrets(namespaceName)

	secretList, err = secretClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

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

	err = updateMatchingSecrets(ctx, k8sClient, authHelpers, workloadCredCfg, common.RegistryAuthConfig{}, secretList.Items, wlIDComponent, namespaceName)
	require.NoError(t, err)
	gotWorkloadSecretNeedsUpdate, err = secretClient.Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"somereg.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("static_username2:static_password2"))+`"},`+
			`"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("authuser_old_foobar:authtoken_old_foobar"))+`"}}}`,
		string(gotWorkloadSecretNeedsUpdate.Data[corev1.DockerConfigJsonKey]),
	)
}

func Test_newUpdateSecretCachedFunc(t *testing.T) {
	logger := logrus.New()
	logger.Out = io.Discard
	log := logrus.NewEntry(logger)
	t.Run("secret no update", func(t *testing.T) {
		// No helper, should return existing auth values.
		authHelpers := []CustomAuthHelper{fakeAuthHelper{}}

		secret := common.RegistryAuthSecret{
			Auths: map[string]common.RegistryAuth{
				"foo.com": {
					Auth: base64.StdEncoding.EncodeToString([]byte("username1:password1")),
				},
			},
		}
		expSecret := common.RegistryAuthSecret{Auths: maps.Clone(secret.Auths)}
		credCache := map[string]map[string]credCacheItem{}
		updateSecret := newUpdateSecretCachedFunc(credCache)
		needsUpdate := updateSecret(t.Context(), log, authHelpers, secret)
		assert.False(t, needsUpdate)
		assert.Equal(t, expSecret, secret)
		if assert.Contains(t, credCache, "foo.com") {
			assert.Contains(t, credCache["foo.com"], secret.Auths["foo.com"].Auth)
		}

		// Try again with cached value.
		needsUpdate = updateSecret(t.Context(), log, authHelpers, secret)
		assert.False(t, needsUpdate)
		assert.Equal(t, expSecret, secret)
	})

	t.Run("secret with update", func(t *testing.T) {
		// No helper, should return existing auth values.
		authHelpers := []CustomAuthHelper{fakeAuthHelper{
			matchers: []string{"foo.com"},
			public:   []bool{false},
			creds: []map[string]map[string]struct {
				username string
				password string
			}{
				{
					"username1": {
						"password1": {
							username: "username_dyn",
							password: "password_dyn",
						},
					},
				},
			},
		}}
		expAuth := base64.StdEncoding.EncodeToString([]byte("username_dyn:password_dyn"))

		secret := common.RegistryAuthSecret{
			Auths: map[string]common.RegistryAuth{
				"foo.com": {
					Auth: base64.StdEncoding.EncodeToString([]byte("username1:password1")),
				},
			},
		}
		nextSecret := common.RegistryAuthSecret{Auths: maps.Clone(secret.Auths)}

		oldSecret := common.RegistryAuthSecret{Auths: maps.Clone(secret.Auths)}
		credCache := map[string]map[string]credCacheItem{}
		updateSecret := newUpdateSecretCachedFunc(credCache)
		needsUpdate := updateSecret(t.Context(), log, authHelpers, secret)
		assert.True(t, needsUpdate)
		assert.NotEqual(t, oldSecret, secret)
		assert.Equal(t, common.RegistryAuthSecret{
			Auths: map[string]common.RegistryAuth{
				"foo.com": {Auth: expAuth},
			},
		}, secret)
		if assert.Contains(t, credCache, "foo.com") {
			assert.Contains(t, credCache["foo.com"], nextSecret.Auths["foo.com"].Auth)
		}

		// Try again with cached value.
		needsUpdate = updateSecret(t.Context(), log, authHelpers, nextSecret)
		assert.True(t, needsUpdate)
		assert.NotEqual(t, oldSecret, nextSecret)
		assert.Equal(t, secret, nextSecret)
	})
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

func (h fakeAuthHelper) Run(ctx context.Context, refURL *url.URL, creds AuthHelperCredentials) (username, password string, err error) {
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
