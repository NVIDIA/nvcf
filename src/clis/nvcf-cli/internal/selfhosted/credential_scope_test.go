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
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// newEntitledNGC accepts each password in access for the repositories under
// the orgs listed for it; "" stands for every repository.
func newEntitledNGC(t *testing.T, access map[string][]string) *fakeNGC {
	t.Helper()
	f := &fakeNGC{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_, pass, _ := r.BasicAuth()
			f.mu.Lock()
			f.sent = append(f.sent, pass)
			failing := f.failing > 0
			f.failing--
			f.mu.Unlock()
			if failing {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			scope := r.URL.Query().Get("scope")
			repo := strings.TrimSuffix(strings.TrimPrefix(scope, "repository:"), ":pull")
			if scope != "" && !strings.Contains(repo, "/") {
				http.Error(w, "malformed token scope", http.StatusBadRequest)
				return
			}
			orgs, known := access[pass]
			if !known {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if scope != "" && !slices.ContainsFunc(orgs, func(org string) bool {
				return org == "" || strings.HasPrefix(repo, org+"/")
			}) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case r.Header.Get("Authorization") == "Bearer t" && strings.HasSuffix(r.URL.Path, "/tags/list"):
			_, _ = w.Write([]byte(`{"tags":["1.0.0","1.2.0"]}`))
		default:
			bearerChallenge(w, "https://"+ngcKeyRegistryHost+"/token")
		}
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	// The test certificate names example.com, not nvcr.io.
	tr.TLSClientConfig.ServerName = "example.com"
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	prev := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = prev })
	return f
}

// mintedPassword returns the password of the one registry entry in a Secret
// this CLI minted.
func mintedPassword(t *testing.T, s *corev1.Secret) string {
	t.Helper()
	require.Equal(t, corev1.SecretTypeDockercfg, s.Type)
	var entries map[string]dockerConfigAuth
	require.NoError(t, json.Unmarshal(s.Data[corev1.DockerConfigKey], &entries))
	require.Len(t, entries, 1)
	for _, e := range entries {
		_, pass, ok := decodeDockerConfigAuth(e)
		require.True(t, ok)
		return pass
	}
	return ""
}

// mintPullSecret resolves the validator's pull secret for image in an empty
// cluster and returns the password it was minted with.
func mintPullSecret(ctx context.Context, t *testing.T, client *fake.Clientset, image, role string) string {
	t.Helper()
	name, _, err := resolveValidatorPullSecret(ctx, client, "", image, role, "runid", false, nil)
	require.NoError(t, err)
	s, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	return mintedPassword(t, s)
}

// Split mode's roles mint their pull secrets in parallel, possibly before the
// registry row has run. Each still gets the credential nvcr.io accepts for
// the image, and the scope is settled once for both.
func TestResolveValidatorPullSecret_BothRolesMintTheAcceptedCredential(t *testing.T) {
	ngc := newFakeNGC(t, "good-key")
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "rotated-out", ""))
	t.Setenv("NGC_API_KEY", "good-key")
	ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))

	roles := []string{clusterValidatorControlPlaneRole, clusterValidatorComputePlaneRole}
	clients := []*fake.Clientset{fake.NewSimpleClientset(), fake.NewSimpleClientset()}
	names, errs := make([]string, len(roles)), make([]error, len(roles))
	var wg sync.WaitGroup
	for i, role := range roles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names[i], _, errs[i] = resolveValidatorPullSecret(ctx, clients[i], "", "nvcr.io/nvidia/cv:1.0.0",
				role, "runid", false, nil)
		}()
	}
	wg.Wait()
	for i, client := range clients {
		require.NoError(t, errs[i], roles[i])
		s, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx, names[i], metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "good-key", mintedPassword(t, s), roles[i])
	}
	sentLogin := 0
	for _, p := range ngc.passwords() {
		if p == "rotated-out" {
			sentLogin++
		}
	}
	assert.Equal(t, 1, sentLogin, "the scope is settled once, with the other role waiting on it")
}

// A docker login with no access to the stack's org gives way to the NGC key
// for that org alone. The validator's pull secret keeps the login, which
// reaches the validator's org, and the 403 is never reported as a rejected
// login.
func TestRegistryCredentials_NoAccessMovesOnlyThatScope(t *testing.T) {
	newEntitledNGC(t, map[string][]string{"login-pass": {"nvidia"}, "key-pass": {"orgb"}})
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "login-pass", ""))
	t.Setenv("NGC_API_KEY", "key-pass")
	ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))

	stack := RegistryEntry{Registry: "nvcr.io", RepoHint: "orgb/team", Critical: true}
	r := registryCredentialCheck(probeRegistryCredential, stack, "nvcr.io/orgb/team", false, nil).Run(ctx)
	assert.True(t, r.Passed, r.Message)
	assert.Equal(t, SeverityInfo, r.Severity)
	assert.Contains(t, r.Message, "has no access to nvcr.io/orgb/team, so the run sends NGC_API_KEY for it")
	assert.NotContains(t, r.Message, "rejected")

	assert.Equal(t, "login-pass", mintPullSecret(ctx, t, fake.NewSimpleClientset(), "nvcr.io/nvidia/cv:1.0.0",
		clusterValidatorComputePlaneRole), "the validator's org is the login's")
}

// With the entitlements the other way round, each row passes on the
// credential that reaches its org, whichever row runs first, and tag
// discovery for the validator image uses the one that reaches the image.
func TestRegistryCredentials_EachScopeKeepsTheCredentialThatReachesIt(t *testing.T) {
	withTempCacheDir(t)
	newEntitledNGC(t, map[string][]string{"login-pass": {"orgb"}, "key-pass": {"nvidia"}})
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "login-pass", ""))
	t.Setenv("NGC_API_KEY", "key-pass")
	ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))

	got, ok := ResolveLatestValidatorTag(ctx, "nvcr.io/nvidia/cv")
	require.True(t, ok)
	assert.Equal(t, "nvcr.io/nvidia/cv:1.2.0", got)
	for _, e := range []RegistryEntry{
		{Registry: "nvcr.io", RepoHint: "nvidia/cv", Critical: true},
		{Registry: "nvcr.io", RepoHint: "orgb/team", Critical: true},
	} {
		r := registryCredentialCheck(probeRegistryCredential, e, e.Registry+"/"+e.RepoHint, false, nil).Run(ctx)
		assert.True(t, r.Passed, r.Message)
		assert.NotContains(t, r.Message, "rejected")
		assert.NotContains(t, r.Message, "generate a new NGC API key")
	}
	assert.Equal(t, "key-pass", mintPullSecret(ctx, t, fake.NewSimpleClientset(), "nvcr.io/nvidia/cv:1.0.0",
		clusterValidatorControlPlaneRole))
}

// A registry fault that leaves a scope's login unjudged is not kept: the next
// lookup asks again and moves to the key once nvcr.io rejects the login.
func TestRegistryCredentials_AnUnjudgedScopeIsSettledAgain(t *testing.T) {
	prevBackoff := registryRetryBackoff
	registryRetryBackoff = time.Millisecond
	t.Cleanup(func() { registryRetryBackoff = prevBackoff })
	ngc := newFakeNGC(t, "good-key")
	ngc.failing = registryRetryAttempts
	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "rotated-out", ""))
	t.Setenv("NGC_API_KEY", "good-key")
	rc := NewRegistryCredentials(false)

	got := rc.lookup(context.Background(), "nvcr.io", "nvidia/cv")
	assert.Equal(t, "rotated-out", got.cred.pass, "an unjudged login stays for this caller")
	got = rc.lookup(context.Background(), "nvcr.io", "nvidia/cv")
	assert.Equal(t, "good-key", got.cred.pass)
	require.NotNil(t, got.rejectedLogin)
}

// After install a refused credential is no longer called this machine's
// alone without looking. An NGC key the cluster's pull secret also holds, as
// up mints it for the local environment, and a docker login, which the
// cluster's pull secret is often made from, are errors. Only a key the
// cluster's pull secrets do not hold warns.
func TestRegistryCredentialCheck_AfterInstallLooksInTheCluster(t *testing.T) {
	newFakeNGC(t, "nothing-matches")
	entry := RegistryEntry{Registry: "nvcr.io", RepoHint: "nvidia/cv", Critical: true}
	inCluster := func(pass string) ClusterPullSecretChecker {
		client := fake.NewSimpleClientset(dockerConfigSecret("nvcr-pull-secret", "nvcf",
			mustDockerConfigJSON(t, "nvcr.io", "$oauthtoken", pass)))
		return func(ctx context.Context, registry, repo, user, secret string) (string, error) {
			return clusterPullSecretHolding(ctx, client, registry, repo, user, secret)
		}
	}
	grade := func(cluster ClusterPullSecretChecker) CheckResult {
		ctx := WithRegistryCredentials(context.Background(), NewRegistryCredentials(false))
		return registryCredentialCheck(probeRegistryCredential, entry, "nvcr.io", true, cluster).Run(ctx)
	}

	dockerHome(t, `{}`)
	t.Setenv("NGC_API_KEY", "revoked-key")
	r := grade(inCluster("revoked-key"))
	assert.Equal(t, SeverityError, r.Severity, r.Message)
	assert.Contains(t, r.Message, "the cluster's pull secret nvcf/nvcr-pull-secret holds the same key")
	r = grade(inCluster("current-key"))
	assert.Equal(t, SeverityWarning, r.Severity, r.Message)
	assert.Contains(t, r.Message, "affects only this machine")

	dockerHome(t, inlineDockerConfig(t, "nvcr.io", "$oauthtoken", "rotated-out", ""))
	t.Setenv("NGC_API_KEY", "")
	r = grade(inCluster("current-key"))
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityError, r.Severity, r.Message)
	assert.NotContains(t, r.Message, "affects only this machine")
}
