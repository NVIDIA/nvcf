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

// resolvePullSecret runs the resolver for image with no explicit secret.
func resolvePullSecret(t *testing.T, client *fake.Clientset, image string) (string, string, error) {
	t.Helper()
	return resolveValidatorPullSecret(context.Background(), client, "", image,
		clusterValidatorControlPlaneRole, "runid", false, nil)
}

func dockerConfigSecret(name, namespace string, cfg []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
}

// mintedKeys returns the registries a Secret this CLI minted or copied, a
// kubernetes.io/dockercfg one, carries credentials for.
func mintedKeys(t *testing.T, s *corev1.Secret) []string {
	t.Helper()
	require.Equal(t, corev1.SecretTypeDockercfg, s.Type)
	var entries map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(s.Data[corev1.DockerConfigKey], &entries))
	var keys []string
	for k := range entries {
		keys = append(keys, k)
	}
	return keys
}

// Keys match the way the kubelet reads them: a scheme or a /v1/ path is
// ignored, and a key with another path covers only repositories under it.
func TestFilterDockerConfig(t *testing.T) {
	for _, tc := range []struct {
		key, registry, repo string
		want                bool
	}{
		{"private.registry.test", "private.registry.test", "a/b", true},
		{"https://private.registry.test", "private.registry.test", "a/b", true},
		{"https://private.registry.test/v1/", "private.registry.test", "a/b", true},
		{"private.registry.test/v2/", "private.registry.test", "a/b", true},
		{"private.registry.test/a", "private.registry.test", "a/b", true},
		{"private.registry.test/ab", "private.registry.test", "a/b", false},
		{"nvcr.io", "private.registry.test", "a/b", false},
		{"https://index.docker.io/v1/", dockerHubRegistry, "library/busybox", true},
	} {
		_, ok := filterDockerConfig(dockerConfigBlob(t, tc.key, "u", "p"), tc.registry, tc.repo)
		assert.Equal(t, tc.want, ok, "%s for %s/%s", tc.key, tc.registry, tc.repo)
	}
	_, ok := filterDockerConfig([]byte("not json"), "private.registry.test", "a")
	assert.False(t, ok)
	noAuths, _ := json.Marshal(map[string]any{"unrelated": "value"})
	_, ok = filterDockerConfig(noAuths, "private.registry.test", "a")
	assert.False(t, ok)
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

// An operator's Secret in default is used as is, without a copy.
func TestResolveValidatorPullSecret_AdoptsOperatorSecretInDefault(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(dockerConfigSecret("operator-secret", "default", cfg))

	name, note, err := resolvePullSecret(t, client, "private.registry.test/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, "operator-secret", name)
	assert.Empty(t, note)
	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Len(t, list.Items, 1, "no copy when the source is already in the validator namespace")
}

// A Secret found elsewhere is copied into default under the run's own name,
// holding only the image registry's entry, and the run says what it copied
// from where.
func TestResolveValidatorPullSecret_CopiesOnlyTheRegistrysEntry(t *testing.T) {
	cfg, err := json.Marshal(map[string]any{"auths": map[string]any{
		"nvcr.io":              map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("a:b"))},
		"private.corp.example": map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("c:d"))},
		"https://index.docker.io/v1/": map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte("e:f")),
		},
	}})
	require.NoError(t, err)
	client := fake.NewSimpleClientset(dockerConfigSecret("regcred", "vault-system", cfg))

	name, note, err := resolvePullSecret(t, client, "nvcr.io/nvidia/cv:1")
	require.NoError(t, err)
	runName := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	assert.Equal(t, runName, name)
	assert.Equal(t, "copied the pull credential for nvcr.io from vault-system/regcred into default/"+runName+
		" for this run", note)

	copied, err := client.CoreV1().Secrets("default").Get(context.Background(), runName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"nvcr.io"}, mintedKeys(t, copied),
		"no other registry's credential is copied")
	src, err := client.CoreV1().Secrets("vault-system").Get(context.Background(), "regcred", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, cfg, src.Data[corev1.DockerConfigJsonKey], "the source is untouched")
}

// The scan asks for docker-registry Secrets only, and falls back to listing
// everything when the server refuses the field selector.
func TestResolveValidatorPullSecret_ListsOnlyDockerConfigSecrets(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "u", "p")
	client := fake.NewSimpleClientset(dockerConfigSecret("creds", "nvcf", cfg))
	var selectors []string
	refused := false
	client.PrependReactor("list", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		sel := action.(ktesting.ListActionImpl).GetListRestrictions().Fields.String()
		selectors = append(selectors, sel)
		if sel != "" && !refused {
			refused = true
			return true, nil, apierrors.NewBadRequest("field selector not supported")
		}
		return false, nil, nil
	})
	name, _, err := resolvePullSecret(t, client, "private.registry.test/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), name)
	require.NotEmpty(t, selectors)
	assert.Equal(t, "type=kubernetes.io/dockerconfigjson", selectors[0])
	assert.Contains(t, selectors, "", "a refused selector falls back to an unfiltered list")
}

func TestResolveValidatorPullSecret_RegistryMismatch(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(dockerConfigSecret("wrong-registry", "nvcf", cfg))
	name, _, err := resolvePullSecret(t, client, "private.registry.test/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, "", name, "secret for a different registry must not match")
}

func TestResolveValidatorPullSecret_PrefersDefaultNamespace(t *testing.T) {
	cfg := dockerConfigBlob(t, "private.registry.test", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(
		dockerConfigSecret("in-default", "default", cfg),
		dockerConfigSecret("in-nvcf", "nvcf", cfg),
	)
	name, _, err := resolvePullSecret(t, client, "private.registry.test/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, "in-default", name)
}

// A Secret created with --docker-server=https://host is the kubelet's
// credential for host, so the scan finds it too.
func TestResolveValidatorPullSecret_MatchesSchemePrefixedKeys(t *testing.T) {
	cfg := dockerConfigBlob(t, "https://nvcr.io", "$oauthtoken", "key")
	client := fake.NewSimpleClientset(dockerConfigSecret("regcred", "nvcf", cfg))
	name, _, err := resolvePullSecret(t, client, "nvcr.io/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid"), name)
}

func TestResolveValidatorPullSecret_FlagOverride(t *testing.T) {
	client := fake.NewSimpleClientset()
	got, note, err := resolveValidatorPullSecret(context.Background(), client, "custom-secret",
		"private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26", clusterValidatorControlPlaneRole, "runid", false,
		nil)
	require.NoError(t, err)
	assert.Equal(t, "custom-secret", got, "explicit override always wins")
	assert.Empty(t, note)

	// No cluster mutation expected on the override path.
	list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
	assert.Empty(t, list.Items)
}

// With no matching cluster Secret, the run mints one from this machine's
// credential for the image's registry, the one the local row checked: the
// NGC key for nvcr.io, a docker login for any other registry.
func TestResolveValidatorPullSecret_MintsTheCredentialTheRowChecked(t *testing.T) {
	runName := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	minted := func(t *testing.T, client *fake.Clientset) *corev1.Secret {
		t.Helper()
		s, err := client.CoreV1().Secrets("default").Get(context.Background(), runName, metav1.GetOptions{})
		require.NoError(t, err)
		return s
	}

	t.Setenv("NVCF_NGC_API_KEY", "nvapi-fallback")
	client := fake.NewSimpleClientset()
	name, note, err := resolvePullSecret(t, client, "nvcr.io/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	assert.Equal(t, runName, name)
	assert.Contains(t, note, "from NVCF_NGC_API_KEY")
	assert.Equal(t, []string{"nvcr.io"}, mintedKeys(t, minted(t, client)))

	dockerHome(t, inlineDockerConfig(t, "stg.nvcr.io", "robot", "pw", ""))
	client = fake.NewSimpleClientset()
	name, note, err = resolvePullSecret(t, client, "stg.nvcr.io/nvidia/cv:1")
	require.NoError(t, err)
	assert.Equal(t, runName, name, "a docker login reaches the Job the row approved it for")
	assert.Contains(t, note, "docker config")
	secret := minted(t, client)
	assert.NotContains(t, string(secret.Data[corev1.DockerConfigKey]), "nvapi-fallback",
		"the NGC key never goes to another registry")
	assert.Equal(t, []string{"stg.nvcr.io"}, mintedKeys(t, secret))
}

// The NGC key is minted for nvcr.io only. Another registry, staging NGC
// included, never gets it.
func TestResolveValidatorPullSecret_NGCKeyOnlyForNvcrIO(t *testing.T) {
	t.Setenv("NGC_API_KEY", "nvapi-test-123")
	for _, image := range []string{"stg.nvcr.io/a/b:1", "registry.nvidia.com:5005/a/b:1", "ghcr.io/a/b:1", "bareimage"} {
		client := fake.NewSimpleClientset()
		got, _, err := resolvePullSecret(t, client, image)
		require.NoError(t, err)
		assert.Empty(t, got, image)
		list, _ := client.CoreV1().Secrets("default").List(context.Background(), metav1.ListOptions{})
		assert.Empty(t, list.Items, "%s must not get the NGC key", image)
	}
}

func TestResolveValidatorPullSecret_AllEmpty(t *testing.T) {
	client := fake.NewSimpleClientset()
	got, _, err := resolvePullSecret(t, client, "private.registry.test/nvidia/nvcf-byoc/cluster-validator:rc26")
	require.NoError(t, err)
	assert.Equal(t, "", got, "no override, no scan match, no local credential -> pulled without a secret")
}

// A refused create is returned with the step that failed, not dropped.
func TestResolveValidatorPullSecret_ReportsARefusedCreate(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "x", errors.New("denied by policy"))
	})
	_, _, err := resolvePullSecret(t, client, "nvcr.io/nvidia/cv:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create pull secret default/")
	assert.Contains(t, err.Error(), "denied by policy")
}

// ModeSplit runs both validators concurrently against contexts that can resolve
// to the same cluster, and runs of one role can overlap. A Secret cannot be
// co-owned, so each role and each run needs its own: with a shared name the
// run that finishes first deletes it while another Job is still pulling, which
// the kubelet reports as FailedToRetrieveImagePullSecret. One run's sweep
// removes its own Secret and leaves the other three.
func TestManagedPullSecret_IsScopedPerRoleAndRun(t *testing.T) {
	ctx := context.Background()
	cfg := dockerConfigBlob(t, "private.registry.test", "user", "pass")
	client := fake.NewSimpleClientset()
	names := map[string]bool{}
	ledgers := map[string]runObjects{}
	for _, role := range []string{clusterValidatorControlPlaneRole, clusterValidatorComputePlaneRole} {
		for _, run := range []string{"run1", "run2"} {
			name := validatorPullSecretRunName(role, run)
			require.False(t, names[name], "%s/%s reuses %s", role, run, name)
			names[name] = true
			ledgers[role+"/"+run] = runObjects{}
			require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
				name, role, run, cfg, false, ledgers[role+"/"+run]))
		}
	}

	deleteCreated(ctx, client, ledgers[clusterValidatorControlPlaneRole+"/run1"], kindSecret)

	secrets := client.CoreV1().Secrets(clusterValidatorNamespace)
	for name := range names {
		_, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if name == validatorPullSecretRunName(clusterValidatorControlPlaneRole, "run1") {
			assert.True(t, apierrors.IsNotFound(err), "the run's own Secret is swept")
			continue
		}
		assert.NoError(t, err, "%s belongs to another role or run and must survive", name)
	}
}

// The minted Secret is the legacy kubernetes.io/dockercfg type. A released
// CLI adopts any kubernetes.io/dockerconfigjson Secret in the namespace whose
// registry matches, labels or not, so it would reuse this run's NGC key and
// then lose it mid-pull when this run's cleanup deletes it.
func TestWriteDockerConfigSecret_IsHiddenFromReleasedScans(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "run1")
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		name, clusterValidatorControlPlaneRole, "run1", cfg, false, nil))

	// What a released CLI's scan matches on.
	list, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, s := range list.Items {
		_, hasRegistry := filterDockerConfig(s.Data[corev1.DockerConfigJsonKey], "nvcr.io", "")
		adoptable := s.Type == corev1.SecretTypeDockerConfigJson && hasRegistry
		assert.False(t, adoptable, "%s must not look like a dockerconfigjson pull secret", s.Name)
	}

	got, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockercfg, got.Type)
	var entries map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	require.NoError(t, json.Unmarshal(got.Data[corev1.DockerConfigKey], &entries))
	assert.Equal(t, "$oauthtoken", entries["nvcr.io"].Username, "the kubelet reads the registry's entry")
	assert.Equal(t, "key", entries["nvcr.io"].Password)
}

// The managed labels a created secret carries must satisfy the sweep selector
// for that role, and must not satisfy another role's.
func TestManagedPullSecret_LabelsMatchOnlyItsOwnRole(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	name := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	require.NoError(t, writeDockerConfigSecret(ctx, client, clusterValidatorNamespace,
		name, clusterValidatorControlPlaneRole, "run1", cfg, false, nil))

	own, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx,
		metav1.ListOptions{LabelSelector: validatorRoleSelector(clusterValidatorControlPlaneRole)})
	require.NoError(t, err)
	assert.Len(t, own.Items, 1, "the secret must match its own role's selector")

	other, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx,
		metav1.ListOptions{LabelSelector: validatorRoleSelector(clusterValidatorComputePlaneRole)})
	require.NoError(t, err)
	assert.Empty(t, other.Items, "it must not match the other role's selector")
}

func TestResolveValidatorPullSecret_RefusesNonNGCRegistry(t *testing.T) {
	t.Setenv("NGC_API_KEY", "nvapi-secret-value")
	client := fake.NewSimpleClientset()

	name, _, err := resolveValidatorPullSecret(context.Background(), client, "",
		"ghcr.io/a/b:1", clusterValidatorControlPlaneRole, "runid", false, nil)
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
func TestResolveValidatorPullSecret_DoesNotAdoptTheOtherRolesSecret(t *testing.T) {
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

	got, _, err := resolveValidatorPullSecret(context.Background(), client, "", "nvcr.io/nvidia/cv:1",
		clusterValidatorComputePlaneRole, "runid", false, nil)
	require.NoError(t, err)
	assert.NotEqual(t, otherRole.Name, got,
		"the compute role must not adopt the control-plane role's managed Secret")
}

// An operator-supplied Secret carries no managed labels, so no sweep touches
// it and adopting it is safe.
func TestResolveValidatorPullSecret_AdoptsOperatorSuppliedSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	operator := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-pull", Namespace: clusterValidatorNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	client := fake.NewSimpleClientset(operator)

	got, _, err := resolveValidatorPullSecret(context.Background(), client, "", "nvcr.io/nvidia/cv:1",
		clusterValidatorComputePlaneRole, "runid", false, nil)
	require.NoError(t, err)
	assert.Equal(t, "operator-pull", got)
}

// No Secret another run minted is adopted, even one of the same role: it may
// hold a stale key, may be deleted by its own run's sweep mid-pull, and a
// --no-cleanup one would otherwise be adopted by every later run.
func TestResolveValidatorPullSecret_NeverAdoptsAnotherRunsSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	secret := func(runID string, preserve bool) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      validatorPullSecretRunName(clusterValidatorComputePlaneRole, runID),
				Namespace: clusterValidatorNamespace,
				Labels:    clusterValidatorRunLabels(clusterValidatorComputePlaneRole, runID, preserve),
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
		}
	}
	client := fake.NewSimpleClientset(secret("earlier", false), secret("kept", true))

	got, _, err := resolveValidatorPullSecret(context.Background(), client, "", "nvcr.io/nvidia/cv:1",
		clusterValidatorComputePlaneRole, "runid", false, nil)
	require.NoError(t, err)
	assert.Empty(t, got, "a managed Secret from another run must not be adopted")
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
		clusterValidatorNamespace, name, clusterValidatorControlPlaneRole, "run1", cfg, false, nil))

	s, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockercfg, s.Type)
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
		clusterValidatorNamespace, name, clusterValidatorControlPlaneRole, "run1", cfg, false, nil)
	require.Error(t, err, "a collision on an unguessable name must never be adopted")
	assert.Contains(t, err.Error(), "refusing to overwrite")

	got, gerr := client.CoreV1().Secrets(clusterValidatorNamespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, gerr)
	assert.Equal(t, corev1.SecretTypeOpaque, got.Type, "the other object must be untouched")
	assert.Contains(t, got.Data, "planted")
	assert.NotContains(t, got.Data, corev1.DockerConfigKey,
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
func TestResolveValidatorPullSecret_MintsUnderTheRunScopedName(t *testing.T) {
	t.Setenv("NGC_API_KEY", "nvapi-secret-value")
	client := fake.NewSimpleClientset()

	got, _, err := resolveValidatorPullSecret(context.Background(), client, "",
		"nvcr.io/nvidia/cv:1", clusterValidatorControlPlaneRole, "a1b2c3d4e5", false, nil)
	require.NoError(t, err)
	assert.Contains(t, got, "a1b2c3d4e5", "the minted secret must be run-scoped")
}

// A released CLI's pull Secret carries managed-by=nvcf-cli and the bare name.
// It is that CLI's to delete, so adopting it breaks a pull mid-flight or
// reuses a stale key forever.
func TestResolveValidatorPullSecret_NeverAdoptsAReleasedCLIsSecret(t *testing.T) {
	cfg := dockerConfigBlob(t, "nvcr.io", "$oauthtoken", "key")
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      validatorPullSecretName,
			Namespace: clusterValidatorNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       clusterValidatorAppLabel,
				"app.kubernetes.io/managed-by": "nvcf-cli",
				"app.kubernetes.io/component":  "preflight",
			},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}
	client := fake.NewSimpleClientset(legacy)
	got, _, err := resolveValidatorPullSecret(context.Background(), client, "", "nvcr.io/nvidia/cv:1",
		clusterValidatorComputePlaneRole, "runid", false, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A --no-cleanup run's Secret carries the preserve marker on both the mirror
// and the mint path, so the orphan sweep keeps it for a day rather than
// 30 minutes, and a re-run of the kept Job can still pull.
func TestPullSecret_PreservedRunIsMarkedOnBothPaths(t *testing.T) {
	for _, name := range ngcAPIKeyEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("NGC_API_KEY", "nvapi-test-123")
	runName := validatorPullSecretRunName(clusterValidatorControlPlaneRole, "runid")
	for _, preserve := range []bool{false, true} {
		mirror := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "chart-owned-creds", Namespace: "nvcf"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: dockerConfigBlob(t, "private.registry.test",
				"$oauthtoken", "key")},
		})
		_, _, err := resolveValidatorPullSecret(context.Background(), mirror, "", "private.registry.test/cv:1",
			clusterValidatorControlPlaneRole, "runid", preserve, nil)
		require.NoError(t, err)
		mint := fake.NewSimpleClientset()
		_, _, err = resolveValidatorPullSecret(context.Background(), mint, "", "nvcr.io/nvidia/cv:1",
			clusterValidatorControlPlaneRole, "runid", preserve, nil)
		require.NoError(t, err)
		for path, client := range map[string]*fake.Clientset{"mirror": mirror, "mint": mint} {
			s, err := client.CoreV1().Secrets("default").Get(context.Background(), runName, metav1.GetOptions{})
			require.NoError(t, err, path)
			assert.Equal(t, preserve, s.Labels[clusterValidatorPreserveLabel] == "true", "%s, preserve=%v", path, preserve)
		}
	}
}
