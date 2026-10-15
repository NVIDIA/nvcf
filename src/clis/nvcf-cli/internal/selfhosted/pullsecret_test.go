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

package selfhosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func dockerConfigBlob(t *testing.T, registry, user, pass string) []byte {
	t.Helper()
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	b, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			registry: map[string]string{
				"username": user,
				"password": pass,
				"auth":     auth,
			},
		},
	})
	require.NoError(t, err)
	return b
}

func mustDockerConfigJSON(t *testing.T, registry, user, pass string) []byte {
	t.Helper()
	b, err := buildDockerConfigJSON(registry, user, pass)
	require.NoError(t, err)
	return b
}

func TestParseRegistryFromImage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"FQDN with tag", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:3.0.0-rc.26", "private.registry.test"},
		{"FQDN with digest", "nvcr.io/foo/bar@sha256:abc", "nvcr.io"},
		{"FQDN latest", "nvcr.io/foo/bar:latest", "nvcr.io"},
		{"localhost with port", "localhost:5000/foo/bar:latest", "localhost:5000"},
		{"localhost no port", "localhost/foo/bar", "localhost"},
		{"docker hub shorthand", "foo/bar:latest", ""},
		{"single segment", "bar", ""},
		{"empty", "", ""},
		{"only slashes", "///", ""},
		{"leading whitespace", "  nvcr.io/foo/bar:latest  ", "nvcr.io"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, parseRegistryFromImage(c.in))
		})
	}
}

func TestDockerConfigHasRegistry(t *testing.T) {
	t.Run("registry present", func(t *testing.T) {
		cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
		assert.True(t, dockerConfigHasRegistry(cfg, "private.registry.test"))
	})
	t.Run("registry absent", func(t *testing.T) {
		cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
		assert.False(t, dockerConfigHasRegistry(cfg, "private.registry.test"))
	})
	t.Run("malformed JSON", func(t *testing.T) {
		assert.False(t, dockerConfigHasRegistry([]byte("not json"), "private.registry.test"))
	})
	t.Run("missing auths key", func(t *testing.T) {
		cfg, _ := json.Marshal(map[string]any{"unrelated": "value"})
		assert.False(t, dockerConfigHasRegistry(cfg, "private.registry.test"))
	})
}

func TestBuildDockerConfigJSON(t *testing.T) {
	cfg := mustDockerConfigJSON(t, "private.registry.test", "$oauthtoken", "test-key")
	var doc struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(cfg, &doc))
	entry, ok := doc.Auths["private.registry.test"]
	require.True(t, ok, "auths must include the registry hostname")
	assert.Equal(t, "$oauthtoken", entry.Username)
	assert.Equal(t, "test-key", entry.Password)

	wantAuth := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:test-key"))
	assert.Equal(t, wantAuth, entry.Auth, "auth field must be base64(username:password)")
}

func TestFirstNonEmptyEnv(t *testing.T) {
	t.Setenv("VALIDATOR_TEST_FIRST", "first-val")
	t.Setenv("VALIDATOR_TEST_SECOND", "")
	t.Setenv("VALIDATOR_TEST_THIRD", "third-val")

	t.Run("returns first non-empty in order", func(t *testing.T) {
		got := firstNonEmptyEnv("VALIDATOR_TEST_SECOND", "VALIDATOR_TEST_FIRST", "VALIDATOR_TEST_THIRD")
		assert.Equal(t, "first-val", got, "second is empty so first hits")
	})
	t.Run("returns empty when all unset", func(t *testing.T) {
		got := firstNonEmptyEnv("VALIDATOR_TEST_NONEXISTENT_A", "VALIDATOR_TEST_NONEXISTENT_B")
		assert.Equal(t, "", got)
	})
}

func TestWriteDockerConfigSecret_Create(t *testing.T) {
	client := fake.NewSimpleClientset()
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", cfg))

	got, err := client.CoreV1().Secrets("default").Get(context.Background(), "nvcr-pull-secret", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type)
	assert.Equal(t, cfg, got.Data[corev1.DockerConfigJsonKey])
	assert.Equal(t, clusterValidatorAppLabel, got.Labels["app.kubernetes.io/name"],
		"label must be attached so the secret is identifiable as CLI-managed")
}

func TestWriteDockerConfigSecret_Update(t *testing.T) {
	old := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "stale-key")
	fresh := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "rotated-key")

	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcr-pull-secret",
			Namespace: "default",
			Labels:    map[string]string{"existing-label": "preserved"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: old},
	})

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", fresh))

	got, _ := client.CoreV1().Secrets("default").Get(context.Background(), "nvcr-pull-secret", metav1.GetOptions{})
	assert.Equal(t, fresh, got.Data[corev1.DockerConfigJsonKey], "data must be updated to the fresh body")
	assert.Equal(t, "preserved", got.Labels["existing-label"], "pre-existing labels must be preserved on update")
	assert.Equal(t, clusterValidatorAppLabel, got.Labels["app.kubernetes.io/name"], "CLI labels must be added on update")
}

// Regression guard for the immutable-Type silent-failure path: a pre-existing
// secret with the wrong Type (but our labels) must be replaced (delete +
// create), not Updated.
func TestWriteDockerConfigSecret_TypeMismatchReplaces(t *testing.T) {
	fresh := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcr-pull-secret",
			Namespace: "default",
			Labels:    clusterValidatorLabels(),
		},
		Type: corev1.SecretTypeOpaque, // wrong type — Update would 422
		Data: map[string][]byte{"some-key": []byte("some-value")},
	})

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", fresh))

	got, err := client.CoreV1().Secrets("default").Get(context.Background(), "nvcr-pull-secret", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, got.Type,
		"secret must be recreated with the correct dockerconfigjson type")
	assert.Equal(t, fresh, got.Data[corev1.DockerConfigJsonKey])
	assert.NotContains(t, got.Data, "some-key", "old Opaque payload must not survive the recreate")
}

// Operator-owned secret with the same name but no CLI labels must NOT be
// destroyed. The replace path is the only place operator data is at risk
// (immutable Type forces delete+create), so the label guard lives there.
func TestWriteDockerConfigSecret_TypeMismatchRefusesUnlabeledSecret(t *testing.T) {
	fresh := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	operatorPayload := []byte("operator-data-do-not-destroy")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcr-pull-secret",
			Namespace: "default",
			// No CLI labels: an operator- or chart-owned secret.
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"operator-payload": operatorPayload},
	})

	err := writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", fresh)
	require.Error(t, err, "must refuse to destroy a non-CLI-managed secret")
	assert.Contains(t, err.Error(), "not managed by nvcf-cli")

	// Original payload must still be intact.
	got, getErr := client.CoreV1().Secrets("default").Get(context.Background(), "nvcr-pull-secret", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, corev1.SecretTypeOpaque, got.Type, "type must be unchanged")
	assert.Equal(t, operatorPayload, got.Data["operator-payload"], "operator data must not be touched")
}

func TestScanAndMirrorPullSecret_FoundInDefault(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-secret", Namespace: "default"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	})

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, "operator-secret", name)

	// Secret in default should not have been duplicated.
	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Len(t, list.Items, 1, "no mirror should happen when the source is already in clusterValidatorNamespace")
}

func TestScanAndMirrorPullSecret_FoundInNvcfMirroredToDefault(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	// Source name deliberately different from validatorPullSecretName
	// to verify the mirror writes under our well-known destination
	// name, not the source name (which could collide with an
	// operator-owned secret in default).
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "chart-owned-creds", Namespace: "nvcf"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	})

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretName, name)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretName, metav1.GetOptions{})
	require.NoError(t, err, "mirror must be created under validatorPullSecretName")
	assert.Equal(t, cfg, mirrored.Data[corev1.DockerConfigJsonKey])

	// The source secret must remain untouched in the source namespace.
	src, err := client.CoreV1().Secrets("nvcf").Get(context.Background(), "chart-owned-creds", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, cfg, src.Data[corev1.DockerConfigJsonKey])
}

func TestScanAndMirrorPullSecret_RegistryMismatch(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-registry", Namespace: "nvcf"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	})

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, "", name, "secret for a different registry must not match")
}

func TestScanAndMirrorPullSecret_NoSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, "", name)
}

func TestScanAndMirrorPullSecret_PrefersDefaultNamespace(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "in-default", Namespace: "default"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "in-nvcf", Namespace: "nvcf"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
		},
	)

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, "in-default", name)
}

func TestAutoCreatePullSecretFromEnv_NoEnv(t *testing.T) {
	// Clear all env vars in the chain.
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, "", got, "no env var set means no secret minted")

	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, list.Items, "no secret must be created when env is empty")
}

func TestAutoCreatePullSecretFromEnv_KeyPresent(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NGC_API_KEY", "nvapi-test-123")

	client := fake.NewSimpleClientset()
	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "private.registry.test")
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretName, got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "private.registry.test"),
		"minted secret must contain auth for the validator image's registry")
}

func TestResolveValidatorPullSecret_FlagOverride(t *testing.T) {
	client := fake.NewSimpleClientset()
	got, err := resolveValidatorPullSecret(context.Background(), client, "custom-secret", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	assert.Equal(t, "custom-secret", got, "explicit override always wins")

	// No cluster mutation expected on the override path.
	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, list.Items)
}

func TestResolveValidatorPullSecret_ScanWins(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "scan-key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-secret", Namespace: "nvcf"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	})

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	// Mirror destination is the validator's well-known name, not the
	// source secret's name (avoids same-name collisions with operator
	// or chart-owned secrets in the destination namespace).
	assert.Equal(t, validatorPullSecretName, got)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(mirrored.Data[corev1.DockerConfigJsonKey], "private.registry.test"))
}

func TestResolveValidatorPullSecret_EnvFallback(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NVCF_NGC_API_KEY", "nvapi-fallback")
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretName, got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "private.registry.test"))
}

func TestResolveValidatorPullSecret_AllEmpty(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	assert.Equal(t, "", got, "no override, no scan match, no env var -> empty so kubelet surfaces ImagePullBackOff")
}

func TestResolveValidatorPullSecret_UnparsableImage(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "this-key-should-not-create-a-secret")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "bareimage")
	require.NoError(t, err)
	assert.Equal(t, "", got, "no registry hostname means no scan target and no auto-create")

	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, list.Items, "no secret must be created when registry cannot be derived")
}
