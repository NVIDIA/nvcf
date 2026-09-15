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
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
)

func TestUpdateMatchingSecrets(t *testing.T) {
	const namespaceName = "sr-4e4aaa7e-eb15-42b7-9bd3-fab7e8fd71ae"
	const wlIDComponent = namespaceName

	workloadSecretNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-" + wlIDComponent + "-regcred-0",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"somereg.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("static_username:static_password")) + `"},` +
					`"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("old_user:old_token")) + `"}}}`,
			),
		},
	}
	workerSecretNeedsUpdate := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-" + wlIDComponent + "-regcred-0",
			Namespace: namespaceName,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(
				`{"auths":{"foobar.com":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("old_worker:old_worker_token")) + `"}}}`,
			),
		},
	}
	otherSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-secret",
			Namespace: namespaceName,
		},
		Data: map[string][]byte{"foo": []byte("bar")},
	}
	k8sClient := k8sfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
		workloadSecretNeedsUpdate.DeepCopy(),
		workerSecretNeedsUpdate.DeepCopy(),
		otherSecret.DeepCopy(),
	)
	authHelpers := []CustomAuthHelper{
		fakeAuthHelper{
			matchers: []string{"foobar.com"},
			public:   []bool{false},
			creds: []map[string]map[string]struct {
				username string
				password string
			}{
				{
					"basicuser": {
						"basicpassword": {
							username: "authuser_new",
							password: "authtoken_new",
						},
					},
					"workeruser": {
						"workerpassword": {
							username: "worker_new",
							password: "worker_token_new",
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
						Auth: base64.StdEncoding.EncodeToString([]byte("static_username:static_password")),
					},
					"foobar.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("basicuser:basicpassword")),
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
						Auth: base64.StdEncoding.EncodeToString([]byte("workeruser:workerpassword")),
					},
				},
			},
		},
	}

	ctx := context.Background()
	secretClient := k8sClient.CoreV1().Secrets(namespaceName)
	secretList, err := secretClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	err = updateMatchingSecrets(ctx, k8sClient, authHelpers, workloadCredCfg, workerCredCfg, secretList.Items, namespaceName, wlIDComponent)
	require.NoError(t, err)

	gotWorkloadSecret, err := secretClient.Get(ctx, workloadSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"somereg.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("static_username:static_password"))+`"},`+
			`"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("authuser_new:authtoken_new"))+`"}}}`,
		string(gotWorkloadSecret.Data[corev1.DockerConfigJsonKey]),
	)
	gotWorkerSecret, err := secretClient.Get(ctx, workerSecretNeedsUpdate.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"auths":{"foobar.com":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("worker_new:worker_token_new"))+`"}}}`,
		string(gotWorkerSecret.Data[corev1.DockerConfigJsonKey]),
	)
	gotOtherSecret, err := secretClient.Get(ctx, otherSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, otherSecret.Data, gotOtherSecret.Data)
}

func Test_newUpdateSecretCachedFunc(t *testing.T) {
	logger := logrus.New()
	logger.Out = io.Discard
	log := logrus.NewEntry(logger)
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
	credCache := map[string]map[string]credCacheItem{}
	updateSecret := newUpdateSecretCachedFunc(credCache)

	needsUpdate := updateSecret(t.Context(), log, authHelpers, secret)
	assert.True(t, needsUpdate)
	assert.Equal(t, common.RegistryAuthSecret{
		Auths: map[string]common.RegistryAuth{
			"foo.com": {Auth: expAuth},
		},
	}, secret)

	needsUpdate = updateSecret(t.Context(), log, authHelpers, nextSecret)
	assert.True(t, needsUpdate)
	assert.Equal(t, secret, nextSecret)
}

func TestCredHelperFallbacksAndStore(t *testing.T) {
	ctx := t.Context()
	h := NewCredHelper()

	username, password, err := h.GetRegistryCredentials(ctx, "registry.example.com/repo:tag", AuthHelperCredentials{
		Username: "static-user",
		Password: "static-pass",
	})
	require.NoError(t, err)
	assert.Equal(t, "static-user", username)
	assert.Equal(t, "static-pass", password)

	const helperName = "unit-test-public"
	RegisterAuthHelper(helperName, fakeAuthHelper{
		matchers: []string{"public.example.com"},
		public:   []bool{true},
		creds:    []map[string]map[string]struct{ username, password string }{{}},
	})
	t.Cleanup(func() {
		customAuthHelpersMu.Lock()
		delete(customAuthHelpers, helperName)
		customAuthHelpersMu.Unlock()
	})

	username, password, err = h.GetRegistryCredentials(ctx, "public.example.com/repo:tag", AuthHelperCredentials{
		Username: "ignored-user",
		Password: "ignored-pass",
	})
	require.NoError(t, err)
	assert.Empty(t, username)
	assert.Empty(t, password)

	store := NewCredentialStore()
	cred, err := store.Get(ctx, "registry.example.com")
	require.NoError(t, err)
	assert.Empty(t, cred)

	err = store.Put(ctx, "registry.example.com", orasauth.Credential{Username: "u", Password: "p"})
	require.NoError(t, err)
	cred, err = store.Get(ctx, "registry.example.com")
	require.NoError(t, err)
	assert.Equal(t, orasauth.Credential{Username: "u", Password: "p"}, cred)

	err = store.Delete(ctx, "registry.example.com")
	require.NoError(t, err)
	cred, err = store.Get(ctx, "registry.example.com")
	require.NoError(t, err)
	assert.Empty(t, cred)
}

func TestRetryableK8sErrors(t *testing.T) {
	conflict := apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "regcred", errors.New("stale"))
	assert.True(t, isRetryableK8sError(conflict))

	assert.True(t, isRetryableK8sError(&apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusTooManyRequests}}))
	assert.True(t, isRetryableK8sError(&apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusInternalServerError}}))
	assert.True(t, isRetryableK8sError(&apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonExpired}}))
	assert.False(t, isRetryableK8sError(errors.New("plain error")))
}

func TestParseAuthErrors(t *testing.T) {
	_, _, err := parseAuth(common.RegistryAuth{Auth: "not-base64"})
	assert.EqualError(t, err, "decode helm registry secret: illegal base64 data at input byte 3")

	_, _, err = parseAuth(common.RegistryAuth{Auth: base64.StdEncoding.EncodeToString([]byte("missing-separator"))})
	assert.EqualError(t, err, "helm registry secret does not contain ':'")
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

func (h fakeAuthHelper) Run(_ context.Context, refURL *url.URL, creds AuthHelperCredentials) (username, password string, err error) {
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
