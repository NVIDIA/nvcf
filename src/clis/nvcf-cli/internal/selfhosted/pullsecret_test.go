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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func TestScanAndMirrorPullSecret_FoundInDefault(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-secret", Namespace: "default"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	})

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), name)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, "", name, "secret for a different registry must not match")
}

func TestScanAndMirrorPullSecret_NoSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
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

	name, err := scanAndMirrorPullSecret(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, "in-default", name)
}

func TestAutoCreatePullSecretFromEnv_NoEnv(t *testing.T) {
	// Clear all env vars in the chain.
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "private.registry.test", clusterValidatorControlPlaneRole, "runid", false)
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
	got, err := autoCreatePullSecretFromEnv(context.Background(), client, "nvcr.io", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "nvcr.io"),
		"minted secret must contain auth for the validator image's registry")
}

func TestResolveValidatorPullSecret_FlagOverride(t *testing.T) {
	client := fake.NewSimpleClientset()
	got, err := resolveValidatorPullSecret(context.Background(), client, "custom-secret", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false)
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

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	// Mirror destination is the validator's well-known name, not the
	// source secret's name (avoids same-name collisions with operator
	// or chart-owned secrets in the destination namespace).
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), got)

	mirrored, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(mirrored.Data[corev1.DockerConfigJsonKey], "private.registry.test"))
}

func TestResolveValidatorPullSecret_EnvFallback(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NVCF_NGC_API_KEY", "nvapi-fallback")
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "nvcr.io/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), got)

	s, err := client.CoreV1().Secrets("default").Get(context.Background(), validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.True(t, dockerConfigHasRegistry(s.Data[corev1.DockerConfigJsonKey], "nvcr.io"))
}

// A mirrored validator image must not receive the NGC key via the env
// fallback: the kubelet would send it to that registry as Basic auth.
func TestResolveValidatorPullSecret_EnvFallbackRefusesMirroredRegistry(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NVCF_NGC_API_KEY", "nvapi-fallback")
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "",
		"private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Empty(t, got, "no secret may be minted for a non-NGC registry")
}

func TestResolveValidatorPullSecret_AllEmpty(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, "", got, "no override, no scan match, no env var -> empty so kubelet surfaces ImagePullBackOff")
}

func TestResolveValidatorPullSecret_UnparsableImage(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "this-key-should-not-create-a-secret")
	}
	client := fake.NewSimpleClientset()

	got, err := resolveValidatorPullSecret(context.Background(), client, "", "bareimage", clusterValidatorControlPlaneRole, "runid", false)
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
	cpName := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	gpuName := validatorPullSecretRoleName("compute-plane")
	require.NotEqual(t, cpName, gpuName, "each role needs its own managed pull secret")

	cfg := dockerConfigBlob(t, "private.registry.test", "user", "pass")
	client := fake.NewSimpleClientset()
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		cpName, clusterValidatorControlPlaneRole, cfg, false))
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		gpuName, "compute-plane", cfg, false))
	// Our labels, but not a name we generate: must survive.
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		"operator-owned-secret", clusterValidatorControlPlaneRole, cfg, false))

	sweepManagedPullSecrets(ctx, client, clusterValidatorControlPlaneRole, "runid")

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
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		name, clusterValidatorControlPlaneRole, cfg, false))

	own, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx,
		metav1.ListOptions{LabelSelector: validatorRoleSelector(clusterValidatorControlPlaneRole)})
	require.NoError(t, err)
	assert.Len(t, own.Items, 1, "the secret must match its own role's selector")

	other, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx,
		metav1.ListOptions{LabelSelector: validatorRoleSelector(clusterValidatorComputePlaneRole)})
	require.NoError(t, err)
	assert.Empty(t, other.Items, "it must not match the other role's selector")
}

func TestAutoCreatePullSecretFromEnv_RefusesNonNGCRegistry(t *testing.T) {
	t.Setenv("NGC_API_KEY", "nvapi-secret-value")
	client := fake.NewSimpleClientset()

	name, err := autoCreatePullSecretFromEnv(context.Background(), client,
		"ghcr.io", clusterValidatorControlPlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Empty(t, name, "no secret may be minted for a non-NGC registry")

	secrets, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(
		context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, secrets.Items, "the NGC key must not reach a third-party registry")
}

// In ModeSplit both kubecontexts can resolve to one cluster. Adopting the
// other role's managed Secret means that role's sweep deletes it while this
// role's pod is still pulling.
func TestScanAndMirrorPullSecret_DoesNotAdoptTheOtherRolesSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	otherRole := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"),
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorRoleLabels(clusterValidatorControlPlaneRole),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	client := fake.NewSimpleClientset(otherRole)

	got, err := scanAndMirrorPullSecret(context.Background(), client, "nvcr.io",
		clusterValidatorComputePlaneRole, "runid", false)
	require.NoError(t, err)
	assert.NotEqual(t, otherRole.Name, got,
		"the compute role must not adopt the control-plane role's managed Secret")
}

// An operator-supplied Secret carries no managed labels, so no sweep touches
// it and adopting it is safe.
func TestScanAndMirrorPullSecret_AdoptsOperatorSuppliedSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	operator := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-pull", Namespace: clusterValidatorNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	client := fake.NewSimpleClientset(operator)

	got, err := scanAndMirrorPullSecret(context.Background(), client, "nvcr.io",
		clusterValidatorComputePlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, "operator-pull", got)
}

// This role's own Secret is still adopted rather than re-minted.
func TestScanAndMirrorPullSecret_AdoptsOwnRoleSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	own := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      validatorPullSecretRunName(clusterValidatorComputePlaneRole, "runid"),
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorRoleLabels(clusterValidatorComputePlaneRole),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	client := fake.NewSimpleClientset(own)

	got, err := scanAndMirrorPullSecret(context.Background(), client, "nvcr.io",
		clusterValidatorComputePlaneRole, "runid", false)
	require.NoError(t, err)
	assert.Equal(t, own.Name, got)
}

// writeDockerConfigSecret is create-only. The name carries this run's
// unguessable suffix, so nothing this CLI created can already hold it and any
// collision is another object. Adopting one would write the NGC credential
// into something we do not own, which label-based ownership cannot prevent:
// the managed labels are three public constants anyone can copy.
func TestWriteDockerConfigSecret_CreatesUnderTheRunScopedName(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	client := fake.NewSimpleClientset()
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")

	require.NoError(t, writeDockerConfigSecret(context.Background(), client,
		clusterValidatorNamespace, name, clusterValidatorControlPlaneRole, cfg, false))

	s, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, s.Type)
	assert.True(t, hasValidatorManagedLabels(s.Labels))
}

func TestWriteDockerConfigSecret_RefusesAnyCollision(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")

	// Even wearing our managed labels: labels are forgeable, the name is not.
	squatter := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: clusterValidatorNamespace,
			Labels: clusterValidatorLabels(),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"planted": []byte("x")},
	}
	client := fake.NewSimpleClientset(squatter)

	err := writeDockerConfigSecret(context.Background(), client,
		clusterValidatorNamespace, name, clusterValidatorControlPlaneRole, cfg, false)
	require.Error(t, err, "a collision on an unguessable name must never be adopted")
	assert.Contains(t, err.Error(), "refusing to overwrite")

	got, gerr := client.CoreV1().Secrets(clusterValidatorNamespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, gerr)
	assert.Equal(t, corev1.SecretTypeOpaque, got.Type, "the other object must be untouched")
	assert.Contains(t, got.Data, "planted")
	assert.NotContains(t, got.Data, corev1.DockerConfigJsonKey,
		"the NGC credential must not be written into an object we do not own")
}

// The unguessable suffix is the actual control. Labels cannot carry it: they
// are three public constants, so a predictable name lets anyone able to create
// a Secret here pre-create it wearing those labels and receive the NGC
// credential. Create-only is only safe because the name cannot be guessed.
func TestValidatorPullSecretRunName_CarriesTheRunID(t *testing.T) {
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "a1b2c3d4e5")
	assert.Contains(t, name, "a1b2c3d4e5",
		"a predictable secret name can be pre-created by someone else")
	assert.NotEqual(t, validatorPullSecretRoleName(clusterValidatorControlPlaneRole), name)

	// Two runs never collide, so one run's sweep cannot take out another's.
	other := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "f6g7h8i9j0")
	assert.NotEqual(t, name, other)
}

// The mint path must use the run-scoped name, not the role name.
func TestAutoCreatePullSecretFromEnv_MintsUnderTheRunScopedName(t *testing.T) {
	t.Setenv("NGC_API_KEY", "nvapi-secret-value")
	client := fake.NewSimpleClientset()

	got, err := autoCreatePullSecretFromEnv(context.Background(), client,
		"nvcr.io", clusterValidatorControlPlaneRole, "a1b2c3d4e5", false)
	require.NoError(t, err)
	assert.Contains(t, got, "a1b2c3d4e5", "the minted secret must be run-scoped")
}
