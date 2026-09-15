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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", clusterValidatorControlPlaneRole, cfg))

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

	// Managed labels plus an unrelated one: only a secret we own may be
	// updated, and updating must not drop labels the operator added.
	existingLabels := clusterValidatorLabels()
	existingLabels["existing-label"] = "preserved"

	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcr-pull-secret",
			Namespace: "default",
			Labels:    existingLabels,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: old},
	})

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", clusterValidatorControlPlaneRole, fresh))

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

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", clusterValidatorControlPlaneRole, fresh))

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

	err := writeDockerConfigSecret(context.Background(), client, "default", "nvcr-pull-secret", clusterValidatorControlPlaneRole, fresh)
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRoleName(clusterValidatorControlPlaneRole), name)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRoleName(clusterValidatorControlPlaneRole), metav1.GetOptions{})
	require.NoError(t, err, "mirror must be created under the role-scoped managed name")
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, "", name, "secret for a different registry must not match")
}

func TestScanAndMirrorPullSecret_NoSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, "in-default", name)
}

func TestAutoCreatePullSecretFromEnv_NoEnv(t *testing.T) {
	// Clear all env vars in the chain.
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
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
	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRoleName(clusterValidatorControlPlaneRole), got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRoleName(clusterValidatorControlPlaneRole), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "private.registry.test"),
		"minted secret must contain auth for the validator image's registry")
}

func TestResolveValidatorPullSecret_FlagOverride(t *testing.T) {
	client := fake.NewSimpleClientset()
	got, err := resolveValidatorPullSecret(context.Background(), client, "custom-secret", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole)
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

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	// Mirror destination is the validator's well-known name, not the
	// source secret's name (avoids same-name collisions with operator
	// or chart-owned secrets in the destination namespace).
	assert.Equal(t, validatorPullSecretRoleName(clusterValidatorControlPlaneRole), got)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRoleName(clusterValidatorControlPlaneRole), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(mirrored.Data[corev1.DockerConfigJsonKey], "private.registry.test"))
}

func TestResolveValidatorPullSecret_EnvFallback(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NVCF_NGC_API_KEY", "nvapi-fallback")
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRoleName(clusterValidatorControlPlaneRole), got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRoleName(clusterValidatorControlPlaneRole), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "private.registry.test"))
}

func TestResolveValidatorPullSecret_AllEmpty(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, "", got, "no override, no scan match, no env var -> empty so kubelet surfaces ImagePullBackOff")
}

func TestResolveValidatorPullSecret_UnparsableImage(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "this-key-should-not-create-a-secret")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "bareimage", clusterValidatorControlPlaneRole)
	require.NoError(t, err)
	assert.Equal(t, "", got, "no registry hostname means no scan target and no auto-create")

	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, list.Items, "no secret must be created when registry cannot be derived")
}

// ModeSplit runs both validators concurrently against contexts that can resolve
// to the same cluster. A Secret cannot be co-owned, so each role needs its own:
// with a shared name the role that finishes first deletes it while the other
// Job is still pulling, which the kubelet reports as
// FailedToRetrieveImagePullSecret.
//
// State-assertable now that the sweep lists and deletes individually; the fake
// clientset implements DeleteCollection as a no-op.
func TestManagedPullSecret_IsScopedPerRole(t *testing.T) {
	ctx := context.Background()
	cpName := validatorPullSecretRoleName(clusterValidatorControlPlaneRole)
	gpuName := validatorPullSecretRoleName("compute-plane")
	require.NotEqual(t, cpName, gpuName, "each role needs its own managed pull secret")

	cfg := dockerConfigBlob(t, "private.registry.test", "user", "pass")
	client := fake.NewSimpleClientset()
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		cpName, clusterValidatorControlPlaneRole, cfg))
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		gpuName, "compute-plane", cfg))
	// Our labels, but not a name we generate: must survive.
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		"operator-owned-secret", clusterValidatorControlPlaneRole, cfg))

	sweepManagedPullSecrets(ctx, client, clusterValidatorControlPlaneRole)

	secrets := client.CoreV1().Secrets(clusterValidatorNamespace)
	_, err := secrets.Get(ctx, cpName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "this role's managed secret must be swept")

	_, err = secrets.Get(ctx, gpuName, metav1.GetOptions{})
	assert.NoError(t, err, "the other role's secret must survive")

	_, err = secrets.Get(ctx, "operator-owned-secret", metav1.GetOptions{})
	assert.NoError(t, err, "matching labels alone must not authorize deleting someone else's secret")
}

// The managed labels a created secret carries must satisfy the sweep selector
// for that role, and must not satisfy another role's.
func TestManagedPullSecret_LabelsMatchOnlyItsOwnRole(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	// dockerConfigBlob encodes the auth value at runtime; a literal base64
	// docker credential in the source trips secret scanners.
	cfg := dockerConfigBlob(t, "private.registry.test", "user", "pass")
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		validatorPullSecretRoleName(clusterValidatorControlPlaneRole),
		clusterValidatorControlPlaneRole, cfg))

	got, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx,
		validatorPullSecretRoleName(clusterValidatorControlPlaneRole), metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, clusterValidatorControlPlaneRole, got.Labels[clusterValidatorRoleLabel],
		"a managed secret must carry its own role label")
	assert.Equal(t, "nvcf-cli", got.Labels["app.kubernetes.io/managed-by"],
		"the managed-by label is what excludes operator-supplied secrets from cleanup")
}

// A matching Secret type is not permission to write. An operator-owned secret
// with a colliding name holds their registry credentials; overwriting it would
// also stamp our managed labels on it, after which the role sweep would delete
// it outright.
func TestWriteDockerConfigSecret_RefusesUnmanagedSameTypeSecret(t *testing.T) {
	ctx := context.Background()
	operatorOwned := dockerConfigBlob(t, "private.registry.test", "operator", "their-secret")
	ours := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "ours")

	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcf-preflight-pull-secret-control-plane",
			Namespace: clusterValidatorNamespace,
			Labels:    map[string]string{"owner": "operator"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: operatorOwned},
	})

	err := writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		"nvcf-preflight-pull-secret-control-plane", clusterValidatorControlPlaneRole, ours)

	require.Error(t, err, "an unmanaged secret of the same type must not be overwritten")
	assert.Contains(t, err.Error(), "not managed by nvcf-cli")

	got, getErr := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx,
		"nvcf-preflight-pull-secret-control-plane", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, operatorOwned, got.Data[corev1.DockerConfigJsonKey],
		"the operator's credentials must be left untouched")
	assert.NotContains(t, got.Labels, "app.kubernetes.io/managed-by",
		"managed labels must not be stamped on it, or the sweep would later delete it")
}

// If an unmanaged Docker-config Secret appears between our Get and our Create,
// Create fails with AlreadyExists and the recovery path refetches it. That path
// must apply the same ownership check as the others, or a lost race silently
// overwrites an operator-owned secret and marks it for sweep deletion.
func TestWriteDockerConfigSecret_RefusesUnmanagedAfterCreateRace(t *testing.T) {
	ctx := context.Background()
	operatorOwned := dockerConfigBlob(t, "private.registry.test", "operator", "their-secret")
	ours := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "ours")

	const name = "nvcf-preflight-pull-secret-control-plane"
	client := fake.NewSimpleClientset()

	// First Get: absent, so the code proceeds to Create. Create: AlreadyExists,
	// as though another actor won the race. Second Get: their unmanaged secret.
	gets := 0
	client.PrependReactor("get", "secrets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
		}
		return true, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: clusterValidatorNamespace,
				Labels:    map[string]string{"owner": "operator"},
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: operatorOwned},
		}, nil
	})
	client.PrependReactor("create", "secrets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(corev1.Resource("secrets"), name)
	})

	var updated bool
	client.PrependReactor("update", "secrets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		updated = true
		return true, nil, nil
	})

	err := writeDockerConfigSecret(ctx, client, clusterValidatorNamespace, name,
		clusterValidatorControlPlaneRole, ours)

	require.Error(t, err, "losing the create race to an unmanaged secret must not overwrite it")
	assert.Contains(t, err.Error(), "not managed by nvcf-cli")
	assert.False(t, updated, "no Update may be issued against an unmanaged secret")
}

// The ownership check reads the Secret and then deletes it by name. In that
// window another actor can delete and recreate it, so the delete has to name
// the exact object that was vetted rather than whatever holds the name by then.
func TestWriteDockerConfigSecret_TypeMismatchDeletePinsTheInspectedObject(t *testing.T) {
	fresh := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "nvcr-pull-secret",
			Namespace:       "default",
			Labels:          clusterValidatorLabels(),
			UID:             "uid-1",
			ResourceVersion: "42",
		},
		Type: corev1.SecretTypeOpaque,
	}
	client := fake.NewSimpleClientset(existing)

	var opts metav1.DeleteOptions
	client.PrependReactor("delete", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		opts = a.(ktesting.DeleteActionImpl).DeleteOptions
		return false, nil, nil
	})

	require.NoError(t, writeDockerConfigSecret(context.Background(), client, "default",
		"nvcr-pull-secret", clusterValidatorControlPlaneRole, fresh))

	require.NotNil(t, opts.Preconditions, "delete must carry preconditions")
	require.NotNil(t, opts.Preconditions.UID)
	require.NotNil(t, opts.Preconditions.ResourceVersion)
	assert.Equal(t, existing.UID, *opts.Preconditions.UID)
	assert.Equal(t, existing.ResourceVersion, *opts.Preconditions.ResourceVersion)
}

// A conflict means the object changed after the ownership check, so the run
// must stop instead of creating over whatever now holds the name.
func TestWriteDockerConfigSecret_TypeMismatchStopsOnDeleteConflict(t *testing.T) {
	fresh := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nvcr-pull-secret",
			Namespace: "default",
			Labels:    clusterValidatorLabels(),
			UID:       "uid-1",
		},
		Type: corev1.SecretTypeOpaque,
	})
	client.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "secrets"}, "nvcr-pull-secret",
			errors.New("UID precondition mismatch"))
	})

	created := false
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		created = true
		return false, nil, nil
	})

	err := writeDockerConfigSecret(context.Background(), client, "default",
		"nvcr-pull-secret", clusterValidatorControlPlaneRole, fresh)
	require.Error(t, err, "a changed object must abort the replace")
	assert.False(t, created, "must not create over an object that was never vetted")
}
