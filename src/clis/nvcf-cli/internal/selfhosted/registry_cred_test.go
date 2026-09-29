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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -- probeRegistryCredential --

func TestProbeRegistryCredential_PublicRegistry(t *testing.T) {
	// Registry returns 200 on /v2/ with the registry API header -> public,
	// no credentials needed.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Replace the default transport with the test server's transport so TLS
	// validation passes against the self-signed cert.
	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = origTransport })

	// Use the test server's host as the registry.
	host := strings.TrimPrefix(srv.URL, "https://")
	err := probeRegistryCredential(context.Background(), host, "", false)
	assert.NoError(t, err, "public registry (200 on /v2/) must not return an error")
}

func TestProbeRegistryCredential_AuthSucceeds(t *testing.T) {
	// Registry: /v2/ returns 401 with WWW-Authenticate; token endpoint returns a token.
	tokenSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token endpoint always succeeds.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"test-token-abc"}`))
	}))
	defer tokenSrv.Close()

	registrySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-token-abc" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Point the client at our token server.
		realm := tokenSrv.URL + "/token"
		w.Header().Set("Www-Authenticate",
			`Bearer realm="`+realm+`",service="test-registry",scope="repository:test:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registrySrv.Close()

	transport := registrySrv.Client().Transport
	origTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = origTransport })

	host := strings.TrimPrefix(registrySrv.URL, "https://")
	err := probeRegistryCredential(context.Background(), host, "", false)
	assert.NoError(t, err, "successful token exchange must return nil")
}

func TestProbeRegistryCredential_AuthFails(t *testing.T) {
	// Registry returns 401 but the token endpoint returns 401 too -> bad credentials.
	tokenSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer tokenSrv.Close()

	registrySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realm := tokenSrv.URL + "/token"
		w.Header().Set("Www-Authenticate",
			`Bearer realm="`+realm+`",service="test-registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer registrySrv.Close()

	transport := registrySrv.Client().Transport
	origTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = origTransport })

	host := strings.TrimPrefix(registrySrv.URL, "https://")
	err := probeRegistryCredential(context.Background(), host, "", false)
	assert.Error(t, err, "failed token exchange must return an error")
}

func TestProbeRegistryCredential_ECRSkipped(t *testing.T) {
	// ECR registries must get a clear "use AWS CLI" message rather than a
	// confusing Bearer token failure.
	err := probeRegistryCredential(context.Background(),
		"123456789.dkr.ecr.us-east-1.amazonaws.com", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ECR", "ECR registries must produce a clear diagnostic")
	assert.Contains(t, err.Error(), "AWS", "message must mention AWS")
}

// -- EnumerateRegistries --

func TestEnumerateRegistries_FromImageRef(t *testing.T) {
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", nil, nil)
	require.NotEmpty(t, entries)

	found := false
	for _, e := range entries {
		if e.Registry == "nvcr.io" {
			assert.True(t, e.Critical, "nvcr.io must be marked critical")
			found = true
		}
	}
	assert.True(t, found, "nvcr.io must appear in the enumerated registries")
}

func TestEnumerateRegistries_RepoHintFromImageRef(t *testing.T) {
	// The RepoHint must be the repo path from the image ref so the token
	// exchange uses the operator's actual org, not a fake one.
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", nil, nil)
	for _, e := range entries {
		if e.Registry == "nvcr.io" {
			assert.Equal(t, "nvidia/nvcf-byoc/cluster-validator", e.RepoHint,
				"RepoHint must carry the actual repo path for correct NGC token scope")
			return
		}
	}
	t.Fatal("nvcr.io not found in entries")
}

func TestEnumerateRegistries_IncludesCertManagerWhenStackIsNotMirroring(t *testing.T) {
	// With no stack values file there is no global.image.registry to mirror
	// cert-manager to, so its upstream registry is genuinely contacted.
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", nil, nil)
	found := false
	for _, e := range entries {
		if e.Registry == "quay.io" {
			assert.False(t, e.Critical, "quay.io must be non-critical")
			found = true
		}
	}
	assert.True(t, found, "quay.io must always be included for cert-manager")
}

func TestEnumerateRegistries_ExtrasAppended(t *testing.T) {
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", nil,
		[]string{"harbor.company.internal:443", "ghcr.io:443"})

	registries := make(map[string]bool, len(entries))
	for _, e := range entries {
		registries[e.Registry] = true
	}
	assert.True(t, registries["harbor.company.internal"], "extra registry must be added")
	assert.True(t, registries["ghcr.io"], "extra registry must be added")
}

func TestEnumerateRegistries_NoDuplicates(t *testing.T) {
	// Pass nvcr.io both as the image registry and as an extra - must not dedup.
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", nil,
		[]string{"nvcr.io"})

	count := 0
	for _, e := range entries {
		if e.Registry == "nvcr.io" {
			count++
		}
	}
	assert.Equal(t, 1, count, "nvcr.io must appear exactly once even when listed twice")
}

func TestEnumerateRegistries_StackValuesFile(t *testing.T) {
	// Write a minimal environments/local.yaml to a temp dir.
	dir := t.TempDir()
	valuesPath := dir + "/local.yaml"
	require.NoError(t, writeFile(valuesPath, []byte(`
global:
  image:
    registry: stg.nvcr.io
`)))

	entries := EnumerateRegistries("nvcr.io/some/image:1.0", []string{valuesPath}, nil)
	found := false
	for _, e := range entries {
		if e.Registry == "stg.nvcr.io" {
			assert.True(t, e.Critical, "NGC staging registry must be critical")
			found = true
		}
	}
	assert.True(t, found, "registry from values file must be included")
}

func TestReadGlobalImageRegistry_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/values.yaml"
	require.NoError(t, writeFile(path, []byte(`
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf-byoc
`)))
	got := LoadStackValues([]string{path}).ImageRegistry
	assert.Equal(t, "nvcr.io", got)
}

func TestReadGlobalImageRegistry_MissingFile(t *testing.T) {
	got := LoadStackValues([]string{"/nonexistent/path/values.yaml"}).ImageRegistry
	assert.Empty(t, got, "missing file must return empty string, not error")
}

func TestReadGlobalImageRegistry_MissingKey(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/values.yaml"
	require.NoError(t, writeFile(path, []byte(`other: value`)))
	got := LoadStackValues([]string{path}).ImageRegistry
	assert.Empty(t, got, "missing global.image.registry must return empty string")
}

// -- isECRRegistry --

func TestIsECRRegistry(t *testing.T) {
	assert.True(t, isECRRegistry("123456789012.dkr.ecr.us-east-1.amazonaws.com"))
	assert.True(t, isECRRegistry("999999999999.dkr.ecr.eu-west-1.amazonaws.com"))
	assert.False(t, isECRRegistry("nvcr.io"))
	assert.False(t, isECRRegistry("ghcr.io"))
	assert.False(t, isECRRegistry("harbor.company.internal"))
	assert.False(t, isECRRegistry("s3.amazonaws.com")) // S3, not ECR
}

// -- registryCredentialCheck binaryCheckSpec --

func TestRegistryCredentialCheck_PassWhenNoError(t *testing.T) {
	checker := func(_ context.Context, reg, _ string, _ bool) error { return nil }
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "nvcr.io", Critical: true})
	r := spec.Run(context.Background())
	assert.True(t, r.Passed)
	assert.Equal(t, "info", r.Severity)
	assert.Contains(t, r.Message, "nvcr.io")
}

func TestRegistryCredentialCheck_CriticalSeverityOnFailure(t *testing.T) {
	checker := func(_ context.Context, reg, _ string, _ bool) error {
		return errorf("credentials rejected")
	}
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "nvcr.io", Critical: true})
	r := spec.Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "error", r.Severity, "critical registry failure must be error severity")
}

func TestRegistryCredentialCheck_WarningSeverityOnNonCriticalFailure(t *testing.T) {
	checker := func(_ context.Context, reg, _ string, _ bool) error {
		return errorf("credentials rejected")
	}
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "quay.io", Critical: false})
	r := spec.Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "warning", r.Severity, "non-critical registry failure must be warning severity")
}

// helpers

func errorf(msg string) error { return fmt.Errorf("%s", msg) }

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// When the stack also mirrors the ACME solver, nothing it installs is pulled
// from quay.io, so probing it is a pointless 10-second round trip.
func TestEnumerateRegistries_SkipsCertManagerWhenSolverIsMirrored(t *testing.T) {
	dir := t.TempDir()
	valuesPath := dir + "/base.yaml"
	require.NoError(t, writeFile(valuesPath, []byte(`
global:
  image:
    registry: mirror.company.internal
certManager:
  acmesolver:
    image:
      repository: mirror.company.internal/jetstack/cert-manager-acmesolver
`)))

	entries := EnumerateRegistries("nvcr.io/some/image:1.0", []string{valuesPath}, nil)
	for _, e := range entries {
		assert.NotEqual(t, certManagerRegistry, e.Registry,
			"a stack mirroring images must not trigger a quay.io probe")
	}
}

// A bare IPv6 literal must stay bracketed or the probe URL is malformed.
func TestEnumerateRegistries_BracketsIPv6Extras(t *testing.T) {
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", nil, []string{"[::1]:5000"})

	found := false
	for _, e := range entries {
		if strings.Contains(e.Registry, "::1") {
			found = true
			assert.Equal(t, "[::1]:5000", e.Registry,
				"an IPv6 literal must keep its brackets so the probe URL parses")
		}
	}
	assert.True(t, found, "the IPv6 extra must be enumerated")
}

// The validator image's registry follows the same Critical policy as every
// other source. Forcing it true makes an air-gapped install fail preflight for
// a registry that is never contacted (ImagePullPolicy is IfNotPresent).
func TestEnumerateRegistries_NonNGCImageRegistryIsNotCritical(t *testing.T) {
	entries := EnumerateRegistries("mirror.company.internal/nvcf/cluster-validator:1.0", nil, nil)
	for _, e := range entries {
		if e.Registry == "mirror.company.internal" {
			assert.False(t, e.Critical, "a non-NGC mirror must not be forced critical")
			return
		}
	}
	t.Fatal("mirror.company.internal not enumerated")
}

// With no validator image and no stack values file, nothing names a registry
// and the run would report on quay.io alone, leaving the operator's NGC
// credentials unchecked in what is the default configuration.
func TestEnumerateRegistries_FallsBackToNGCWhenNothingNamesARegistry(t *testing.T) {
	got := EnumerateRegistries("", nil, nil)

	var ngc *RegistryEntry
	for i := range got {
		if got[i].Registry == "nvcr.io" {
			ngc = &got[i]
		}
	}
	require.NotNil(t, ngc, "nvcr.io must be probed when no source names a registry")
	assert.False(t, ngc.Critical,
		"a guessed registry must not hard-fail the run; this category has no opt-out flag")
}

// A mirrored install has no reason to reach nvcr.io, and probing it there adds
// a failing round trip for the sites least able to make it.
func TestEnumerateRegistries_NoNGCFallbackForAMirroredStack(t *testing.T) {
	dir := t.TempDir()
	values := filepath.Join(dir, "base.yaml")
	require.NoError(t, os.WriteFile(values,
		[]byte("global:\n  image:\n    registry: harbor.example.com\n"), 0o600))

	got := EnumerateRegistries("", []string{values}, nil)
	for _, e := range got {
		assert.NotEqual(t, "nvcr.io", e.Registry,
			"a stack that named its own registry must not be probed against NGC")
	}
}

// The fallback must not shadow or duplicate an NGC registry a source named,
// which carries a repo hint and is critical.
func TestEnumerateRegistries_FallbackDoesNotDuplicateNamedNGC(t *testing.T) {
	got := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0", nil, nil)

	n := 0
	for _, e := range got {
		if e.Registry == "nvcr.io" {
			n++
			assert.True(t, e.Critical, "an NGC registry named by the image stays critical")
			assert.Equal(t, "nvidia/nvcf-byoc/cluster-validator", e.RepoHint)
		}
	}
	assert.Equal(t, 1, n, "nvcr.io must appear exactly once")
}

// parseRegistryHostPort returns its input verbatim when SplitHostPort fails,
// and it fails on an already-bracketed literal with missingPort. Without a
// prefix guard that arrives here still bracketed and becomes "[[fd00::1]]",
// which http.NewRequest rejects. The documented bracketed form must work.
func TestEnumerateRegistries_DoesNotDoubleBracketIPv6(t *testing.T) {
	for _, in := range []string{"[fd00::1]", "fd00::1"} {
		got := EnumerateRegistries("", nil, []string{in})
		var found string
		for _, e := range got {
			if strings.Contains(e.Registry, "fd00") {
				found = e.Registry
			}
		}
		assert.Equal(t, "[fd00::1]", found, "input %q must yield a singly-bracketed host", in)
	}
}

func TestEnumerateRegistries_KeepsBracketedIPv6WithPort(t *testing.T) {
	got := EnumerateRegistries("", nil, []string{"[fd00::1]:5000"})
	var found string
	for _, e := range got {
		if strings.Contains(e.Registry, "fd00") {
			found = e.Registry
		}
	}
	assert.Equal(t, "[fd00::1]:5000", found)
}

// The probe builds "https://" + registry + "/v2/", so a registry string that
// carries "/" or "@" moves the host. A values file the CLI finds by walking up
// from CWD must not be able to aim an outbound request.
func TestEnumerateRegistries_RejectsHostMovingRegistryStrings(t *testing.T) {
	dir := t.TempDir()
	values := filepath.Join(dir, "base.yaml")
	require.NoError(t, os.WriteFile(values,
		[]byte("global:\n  image:\n    registry: nvcr.io@attacker.example.com\n"), 0o600))

	for _, e := range EnumerateRegistries("nvcr.io@evil.test/x/y:1", []string{values}, nil) {
		assert.NotContains(t, e.Registry, "@", "a host-moving string must not be probed")
		assert.NotContains(t, e.Registry, "attacker.example.com")
		assert.NotContains(t, e.Registry, "evil.test")
	}
}

// helmfile layers the environment file over base.yaml, so an env file that
// overrides only the repository keeps base.yaml's registry, and an env file
// that sets a mirror replaces it.
func TestLoadStackValues_LayersEnvOverBase(t *testing.T) {
	dir := t.TempDir()
	base, env := dir+"/base.yaml", dir+"/airgap.yaml"
	require.NoError(t, writeFile(base, []byte(`
global:
  image:
    registry: nvcr.io
    repository: nvidia/nvcf-byoc
ingress:
  gatewayApi:
    controllerNamespace: envoy-gateway-system
`)))
	require.NoError(t, writeFile(env, []byte(`
global:
  image:
    repository: other/repo
`)))
	got := LoadStackValues([]string{base, env})
	assert.True(t, got.Found)
	assert.Equal(t, "nvcr.io", got.ImageRegistry, "an env file that only overrides the repository keeps the base registry")
	assert.Equal(t, "envoy-gateway-system", got.EnvoyGatewayNamespace)

	require.NoError(t, writeFile(env, []byte(`
global:
  image:
    registry: harbor.corp.example
`)))
	assert.Equal(t, "harbor.corp.example", LoadStackValues([]string{base, env}).ImageRegistry)
}

// A registry value carrying a path is valid for the stack, which renders
// registry + "/" + repository. It must still be probed, and it must still
// suppress the nvcr.io guess, rather than dropping the whole category.
func TestEnumerateRegistries_StackRegistryWithPath(t *testing.T) {
	dir := t.TempDir()
	values := dir + "/base.yaml"
	require.NoError(t, writeFile(values, []byte(`
global:
  image:
    registry: harbor.corp.example/nvcf
certManager:
  acmesolver:
    image:
      repository: harbor.corp.example/jetstack/cert-manager-acmesolver
`)))
	got := EnumerateRegistries("", []string{values}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "harbor.corp.example", got[0].Registry)
	assert.Equal(t, "nvcf", got[0].RepoHint)
}

// A rejected registry value must not count as a source naming a registry:
// otherwise it suppresses the nvcr.io fallback and the category vanishes.
func TestEnumerateRegistries_RejectedValueDoesNotSuppressFallback(t *testing.T) {
	dir := t.TempDir()
	values := dir + "/base.yaml"
	require.NoError(t, writeFile(values, []byte("global:\n  image:\n    registry: \"nvcr.io@evil.test\"\n")))
	got := EnumerateRegistries("", []string{values}, nil)
	var names []string
	for _, e := range got {
		names = append(names, e.Registry)
	}
	assert.Contains(t, names, ngcRegistry)
	assert.NotContains(t, names, "nvcr.io@evil.test")
}

// quay.io depends on the ACME solver image, which the stack leaves on
// quay.io/jetstack unless it is overridden, not on whether a values file was
// found.
func TestEnumerateRegistries_QuayFollowsTheACMESolverImage(t *testing.T) {
	dir := t.TempDir()
	values := dir + "/base.yaml"
	names := func() []string {
		var out []string
		for _, e := range EnumerateRegistries("", []string{values}, nil) {
			out = append(out, e.Registry)
		}
		return out
	}
	require.NoError(t, writeFile(values, []byte("global:\n  image:\n    registry: harbor.corp.example\n")))
	assert.Contains(t, names(), certManagerRegistry, "a mirrored stack still pulls the default ACME solver from quay.io")

	require.NoError(t, writeFile(values, []byte(
		"global:\n  image:\n    registry: harbor.corp.example\ncertManager:\n  acmesolver:\n    image:\n      repository: harbor.corp.example/jetstack/acmesolver\n")))
	assert.NotContains(t, names(), certManagerRegistry, "a mirrored ACME solver never reaches quay.io")
}

// "[fd00::1]" with no port must reach the ConfigMap unbracketed; the validator
// brackets it itself, and a double bracket is never reachable.
func TestParseRegistryHostPort_StripsIPv6Brackets(t *testing.T) {
	host, port := parseRegistryHostPort("[fd00::1]")
	assert.Equal(t, "fd00::1", host)
	assert.Equal(t, 443, port)
	got := EnumerateRegistries("", nil, []string{"[fd00::1]"})
	var names []string
	for _, e := range got {
		names = append(names, e.Registry)
	}
	assert.Contains(t, names, "[fd00::1]")
}

// A validator image whose registry fails validation must not count as naming
// a registry either, or the nvcr.io fallback is suppressed with nothing probed.
func TestEnumerateRegistries_RejectedImageRegistryDoesNotSuppressFallback(t *testing.T) {
	var names []string
	for _, e := range EnumerateRegistries("user@evil.test/nvidia/validator:1", nil, nil) {
		names = append(names, e.Registry)
	}
	assert.Contains(t, names, ngcRegistry)
}

// A critical registry whose /v2/ answers 200 lets anyone in, so a configured
// credential cannot be checked there. The result says so instead of
// "credentials valid", and the check still passes.
func TestProbeRegistryCredential_CriticalAnonymousIsNotVerified(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = origTransport })
	host := strings.TrimPrefix(srv.URL, "https://")
	dockerHome(t, inlineDockerConfig(t, host, "u", "p", ""))

	err := probeRegistryCredential(context.Background(), host, "", true)
	var notVerified errRegistryCredentialsNotVerified
	require.ErrorAs(t, err, &notVerified)

	r := registryCredentialCheck(func(ctx context.Context, reg, repo string, critical bool) error {
		return probeRegistryCredential(ctx, reg, repo, critical)
	}, RegistryEntry{Registry: host, Critical: true}).Run(context.Background())
	assert.True(t, r.Passed)
	assert.NotContains(t, r.Message, "credentials valid")
	assert.Contains(t, r.Message, "not verified")
}
