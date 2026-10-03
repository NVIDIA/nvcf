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
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
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
	var outcome registryProbeOutcome
	require.ErrorAs(t, err, &outcome)
	assert.Equal(t, probeAnonymous, outcome.kind, "no credential was sent, so none is reported valid")

	r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: host}, host, false).
		Run(context.Background())
	assert.True(t, r.Passed)
	assert.Equal(t, SeverityInfo, r.Severity)
	assert.Contains(t, r.Message, "reachable anonymously; no local credential checked")
	assert.NotContains(t, r.Message, "credentials valid")
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
	var outcome registryProbeOutcome
	require.ErrorAs(t, err, &outcome, "an anonymous token is not a valid credential")
	assert.Equal(t, probeAnonymous, outcome.kind)

	dockerHome(t, inlineDockerConfig(t, host, "u", "p", ""))
	err = probeRegistryCredential(context.Background(), host, "", false)
	assert.NoError(t, err, "a token issued for a credential that was sent is a valid credential")
	r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: host}, host, false).
		Run(context.Background())
	assert.Contains(t, r.Message, "credentials valid")
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
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", StackValues{}, nil)
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
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", StackValues{}, nil)
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
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", StackValues{}, nil)
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
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", StackValues{},
		parseExtras(t, "harbor.company.internal:443", "ghcr.io:443"))

	registries := make(map[string]bool, len(entries))
	for _, e := range entries {
		registries[e.Registry] = true
	}
	assert.True(t, registries["harbor.company.internal"], "extra registry must be added")
	assert.True(t, registries["ghcr.io"], "extra registry must be added")
}

func TestEnumerateRegistries_NoDuplicates(t *testing.T) {
	// Pass nvcr.io both as the image registry and as an extra - must not dedup.
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", StackValues{}, parseExtras(t, "nvcr.io"))

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

	entries := EnumerateRegistries("nvcr.io/some/image:1.0", LoadStackValues([]string{valuesPath}), nil)
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
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "nvcr.io", Critical: true}, "nvcr.io", false)
	r := spec.Run(context.Background())
	assert.True(t, r.Passed)
	assert.Equal(t, SeverityInfo, r.Severity)
	assert.Contains(t, r.Message, "nvcr.io")
}

func TestRegistryCredentialCheck_CriticalSeverityOnFailure(t *testing.T) {
	checker := func(_ context.Context, reg, _ string, _ bool) error {
		return errorf("credentials rejected")
	}
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "nvcr.io", Critical: true}, "nvcr.io", false)
	r := spec.Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityError, r.Severity, "critical registry failure must be error severity")
}

func TestRegistryCredentialCheck_WarningSeverityOnNonCriticalFailure(t *testing.T) {
	checker := func(_ context.Context, reg, _ string, _ bool) error {
		return errorf("credentials rejected")
	}
	spec := registryCredentialCheck(checker, RegistryEntry{Registry: "quay.io", Critical: false}, "quay.io", false)
	r := spec.Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityWarning, r.Severity, "non-critical registry failure must be warning severity")
}

// helpers

// parseExtras parses --cluster-validator-registries entries.
func parseExtras(t *testing.T, entries ...string) []RegistryEntry {
	t.Helper()
	var out []RegistryEntry
	for _, e := range entries {
		entry, err := ParseRegistryExtra(e)
		require.NoError(t, err, e)
		out = append(out, entry)
	}
	return out
}

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

	entries := EnumerateRegistries("nvcr.io/some/image:1.0", LoadStackValues([]string{valuesPath}), nil)
	for _, e := range entries {
		assert.NotEqual(t, certManagerRegistry, e.Registry,
			"a stack mirroring images must not trigger a quay.io probe")
	}
}

// A bare IPv6 literal must stay bracketed or the probe URL is malformed.
func TestEnumerateRegistries_BracketsIPv6Extras(t *testing.T) {
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", StackValues{}, parseExtras(t, "[::1]:5000"))

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
	entries := EnumerateRegistries("mirror.company.internal/nvcf/cluster-validator:1.0", StackValues{}, nil)
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
	got := EnumerateRegistries("", StackValues{}, nil)

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

	got := EnumerateRegistries("", LoadStackValues([]string{values}), nil)
	for _, e := range got {
		assert.NotEqual(t, "nvcr.io", e.Registry,
			"a stack that named its own registry must not be probed against NGC")
	}
}

// The fallback must not shadow or duplicate an NGC registry a source named,
// which carries a repo hint and is critical.
func TestEnumerateRegistries_FallbackDoesNotDuplicateNamedNGC(t *testing.T) {
	got := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0", StackValues{}, nil)

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

// An IPv6 literal, bracketed or not, is listed once with one pair of
// brackets. A value that arrived still bracketed and was bracketed again
// became "[[fd00::1]]", which http.NewRequest rejects.
func TestEnumerateRegistries_DoesNotDoubleBracketIPv6(t *testing.T) {
	for _, in := range []string{"[fd00::1]", "fd00::1"} {
		got := EnumerateRegistries("", StackValues{}, parseExtras(t, in))
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
	got := EnumerateRegistries("", StackValues{}, parseExtras(t, "[fd00::1]:5000"))
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

	for _, e := range EnumerateRegistries("nvcr.io@evil.test/x/y:1", LoadStackValues([]string{values}), nil) {
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

// global.storageClass is the class every stack PVC names; an environment file
// sets it over base.yaml's empty default.
func TestLoadStackValues_StorageClass(t *testing.T) {
	dir := t.TempDir()
	base, env := filepath.Join(dir, "base.yaml"), filepath.Join(dir, "prod.yaml")
	require.NoError(t, writeFile(base, []byte("global:\n  storageClass: \"\"\n")))
	require.NoError(t, writeFile(env, []byte("global:\n  storageClass: fast-ssd\n")))
	assert.Empty(t, LoadStackValues([]string{base}).StorageClass)
	assert.Equal(t, "fast-ssd", LoadStackValues([]string{base, env}).StorageClass)
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
	got := EnumerateRegistries("", LoadStackValues([]string{values}), nil)
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
	got := EnumerateRegistries("", LoadStackValues([]string{values}), nil)
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
		for _, e := range EnumerateRegistries("", LoadStackValues([]string{values}), nil) {
			out = append(out, e.Registry)
		}
		return out
	}
	require.NoError(t, writeFile(values, []byte(
		"global:\n  image:\n    registry: harbor.corp.example\ncertManager:\n  enabled: true\n")))
	assert.Contains(t, names(), certManagerRegistry, "a mirrored stack still pulls the default ACME solver from quay.io")

	require.NoError(t, writeFile(values, []byte(
		"global:\n  image:\n    registry: harbor.corp.example\ncertManager:\n  enabled: true\n"+
			"  acmesolver:\n    image:\n      repository: harbor.corp.example/jetstack/acmesolver\n")))
	assert.NotContains(t, names(), certManagerRegistry, "a mirrored ACME solver never reaches quay.io")

	// The cert-manager release's condition is a YAML boolean: off, or a
	// quoted "true", installs no cert-manager and no solver.
	for _, enabled := range []string{"false", `"true"`} {
		require.NoError(t, writeFile(values, []byte(
			"global:\n  image:\n    registry: harbor.corp.example\ncertManager:\n  enabled: "+enabled+"\n")))
		assert.NotContains(t, names(), certManagerRegistry, "certManager.enabled: %s", enabled)
	}
}

// "[fd00::1]" with no port must reach the ConfigMap unbracketed; the validator
// brackets it itself, and a double bracket is never reachable.
func TestParseRegistryHostPort_StripsIPv6Brackets(t *testing.T) {
	host, port := parseRegistryHostPort("[fd00::1]")
	assert.Equal(t, "fd00::1", host)
	assert.Equal(t, 443, port)
	got := EnumerateRegistries("", StackValues{}, parseExtras(t, "[fd00::1]"))
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
	for _, e := range EnumerateRegistries("user@evil.test/nvidia/validator:1", StackValues{}, nil) {
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
	var outcome registryProbeOutcome
	require.ErrorAs(t, err, &outcome)
	assert.Equal(t, probeNotVerified, outcome.kind)

	r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: host, Critical: true}, host, false).
		Run(context.Background())
	assert.True(t, r.Passed)
	assert.NotContains(t, r.Message, "credentials valid")
	assert.Contains(t, r.Message, "not verified")
}

// The Gateways forwarded are the ones the stack wires NVCF routes to, with
// the gates its template applies: none without ingress.gatewayApi.enabled,
// shared and grpc always, nats only with its route on, and llmGrpc and
// llmQuic only with the LLM worker route on. An environment file can name a
// Gateway whose route stays off, and the stack never creates that Gateway.
func TestLoadStackValues_Gateways(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/base.yaml"
	require.NoError(t, writeFile(base, []byte(`
ingress:
  gatewayApi:
    enabled: true
    gateways:
      shared: {name: "", namespace: ""}
      grpc: {name: "", namespace: ""}
      llmGrpc: {name: "", namespace: ""}
`)))
	const named = `
      shared: {name: shared-gw, namespace: envoy-gateway-system}
      grpc: {name: grpc-gw, namespace: envoy-gateway-system}
      nats: {name: nats-gw, namespace: envoy-gateway-system}
      llmGrpc: {name: llm-grpc-gateway, namespace: envoy-gateway}
      llmQuic: {name: llm-quic-gateway, namespace: envoy-gateway}
`
	for name, tc := range map[string]struct {
		env  string
		want []string
	}{
		"routes off": {
			env:  "ingress:\n  gatewayApi:\n    gateways:" + named,
			want: []string{"envoy-gateway-system/grpc-gw", "envoy-gateway-system/shared-gw"},
		},
		"routes on": {
			env: "ingress:\n  gatewayApi:\n    routes:\n      nats: {enabled: true}\n      llmWorker: {enabled: true}\n" +
				"    gateways:" + named,
			want: []string{
				"envoy-gateway-system/grpc-gw", "envoy-gateway-system/nats-gw", "envoy-gateway-system/shared-gw",
				"envoy-gateway/llm-grpc-gateway", "envoy-gateway/llm-quic-gateway",
			},
		},
		"gateway API off": {
			env:  "ingress:\n  gatewayApi:\n    enabled: false\n    gateways:" + named,
			want: nil,
		},
	} {
		env := dir + "/" + strings.ReplaceAll(name, " ", "-") + ".yaml"
		require.NoError(t, writeFile(env, []byte(tc.env)), name)
		assert.Equal(t, tc.want, LoadStackValues([]string{base, env}).Gateways, name)
	}

	assert.Empty(t, LoadStackValues([]string{base}).Gateways, "an entry left empty is one the install does not use")
}

// fakeRegistry is a TLS registry whose /v2/ and token endpoint answer as the
// test says. Its host is the registry name, and every client in the process
// trusts its certificate for the test.
type fakeRegistry struct {
	host       string
	tokenCalls int
	authSent   []string
}

func newFakeRegistry(
	t *testing.T, v2 func(w http.ResponseWriter, realm string), token func(w http.ResponseWriter),
) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			f.tokenCalls++
			f.authSent = append(f.authSent, r.Header.Get("Authorization"))
			token(w)
			return
		}
		v2(w, srv.URL+"/token")
	}))
	t.Cleanup(srv.Close)
	prev := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = prev })
	f.host = strings.TrimPrefix(srv.URL, "https://")
	return f
}

func bearerChallenge(w http.ResponseWriter, realm string) {
	w.Header().Set("Www-Authenticate", `Bearer realm="`+realm+`",service="test"`)
	w.WriteHeader(http.StatusUnauthorized)
}

func tokenStatus(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"token":"t"}`))
		}
	}
}

// Every grading guard of the registry probe, driven through the real probe
// and the real row: exactly one outcome, a credential the token endpoint
// refused, fails a critical registry. A credential that cannot be checked,
// is missing, or meets a registry this probe cannot speak to is a warning or
// an informational pass.
func TestRegistryCredentialCheck_Grading(t *testing.T) {
	prevBackoff := registryRetryBackoff
	registryRetryBackoff = time.Millisecond
	t.Cleanup(func() { registryRetryBackoff = prevBackoff })

	type want struct {
		passed   bool
		severity Severity
		message  string
	}
	for name, tc := range map[string]struct {
		v2       func(w http.ResponseWriter, realm string)
		token    func(w http.ResponseWriter)
		withCred bool
		want     want
	}{
		"credential accepted": {
			v2: bearerChallenge, token: tokenStatus(http.StatusOK), withCred: true,
			want: want{true, SeverityInfo, "credentials valid"},
		},
		"credential rejected with 401": {
			v2: bearerChallenge, token: tokenStatus(http.StatusUnauthorized), withCred: true,
			want: want{false, SeverityError, "rejected"},
		},
		"credential rejected with 403": {
			v2: bearerChallenge, token: tokenStatus(http.StatusForbidden), withCred: true,
			want: want{false, SeverityError, "rejected"},
		},
		"no credential, anonymous token refused": {
			v2: bearerChallenge, token: tokenStatus(http.StatusUnauthorized),
			want: want{false, SeverityWarning, "no local credentials found"},
		},
		"no credential, anonymous token issued": {
			v2: bearerChallenge, token: tokenStatus(http.StatusOK),
			want: want{false, SeverityWarning, "no local credentials found"},
		},
		"token service failing": {
			v2: bearerChallenge, token: tokenStatus(http.StatusServiceUnavailable), withCred: true,
			want: want{false, SeverityWarning, "could not verify credentials from this machine"},
		},
		"token service throttling": {
			v2: bearerChallenge, token: tokenStatus(http.StatusTooManyRequests), withCred: true,
			want: want{false, SeverityWarning, "could not verify credentials from this machine"},
		},
		"registry failing": {
			v2: func(w http.ResponseWriter, _ string) {
				w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
				w.WriteHeader(http.StatusBadGateway)
			},
			withCred: true,
			want:     want{false, SeverityWarning, "could not verify credentials from this machine"},
		},
		"proxy page instead of a registry": {
			v2:       func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusForbidden) },
			withCred: true,
			want:     want{true, SeverityInfo, "did not identify as an OCI registry"},
		},
		"captive portal 200": {
			v2:       func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusOK) },
			withCred: true,
			want:     want{true, SeverityInfo, "did not identify as an OCI registry"},
		},
		"basic auth": {
			v2: func(w http.ResponseWriter, _ string) {
				w.Header().Set("Www-Authenticate", `Basic realm="harbor"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			withCred: true,
			want:     want{true, SeverityInfo, "uses Basic auth"},
		},
		"negotiate then bearer": {
			v2: func(w http.ResponseWriter, realm string) {
				w.Header().Add("Www-Authenticate", "Negotiate")
				w.Header().Add("Www-Authenticate", `Bearer realm="`+realm+`"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			token: tokenStatus(http.StatusUnauthorized), withCred: true,
			want: want{false, SeverityError, "rejected"},
		},
		"realm on another host": {
			v2: func(w http.ResponseWriter, _ string) {
				bearerChallenge(w, "https://attacker.example/token")
			},
			withCred: true,
			want:     want{true, SeverityInfo, "refusing to send credentials"},
		},
		"no realm off nvcr.io": {
			v2: func(w http.ResponseWriter, _ string) {
				w.Header().Set("Www-Authenticate", "Bearer")
				w.WriteHeader(http.StatusUnauthorized)
			},
			withCred: true,
			want:     want{true, SeverityInfo, "named no Bearer token realm"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			token := tc.token
			if token == nil {
				token = func(w http.ResponseWriter) { t.Error("the token endpoint must not be called") }
			}
			reg := newFakeRegistry(t, tc.v2, token)
			if tc.withCred {
				dockerHome(t, inlineDockerConfig(t, reg.host, "u", "p", ""))
			} else {
				dockerHome(t, `{}`)
			}
			r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: reg.host, Critical: true},
				reg.host, false).Run(context.Background())
			assert.Equal(t, tc.want.passed, r.Passed, r.Message)
			assert.Equal(t, tc.want.severity, r.Severity, r.Message)
			assert.Contains(t, r.Message, tc.want.message)
		})
	}

	t.Run("ECR", func(t *testing.T) {
		const ecr = "123456789012.dkr.ecr.us-west-2.amazonaws.com"
		r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: ecr, Critical: true}, ecr, false).
			Run(context.Background())
		assert.True(t, r.Passed)
		assert.Equal(t, SeverityInfo, r.Severity)
		assert.Contains(t, r.Message, "aws ecr get-login-password --region us-west-2 | "+
			"docker login --username AWS --password-stdin "+ecr)
	})

	t.Run("unreachable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		host := ln.Addr().String()
		require.NoError(t, ln.Close())
		r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: host, Critical: true}, host, false).
			Run(context.Background())
		assert.False(t, r.Passed)
		assert.Equal(t, SeverityWarning, r.Severity, "a workstation that cannot reach a registry does not block")
		assert.Contains(t, r.Message, "could not verify credentials from this machine")
	})

	t.Run("non-critical rejection", func(t *testing.T) {
		reg := newFakeRegistry(t, bearerChallenge, tokenStatus(http.StatusUnauthorized))
		dockerHome(t, inlineDockerConfig(t, reg.host, "u", "p", ""))
		r := registryCredentialCheck(probeRegistryCredential, RegistryEntry{Registry: reg.host}, reg.host, false).
			Run(context.Background())
		assert.False(t, r.Passed)
		assert.Equal(t, SeverityWarning, r.Severity)
	})
}

// A 5xx or 429 is retried before it is graded, honoring Retry-After.
func TestProbeRegistryCredential_RetriesTransientFailures(t *testing.T) {
	prevBackoff := registryRetryBackoff
	registryRetryBackoff = time.Millisecond
	t.Cleanup(func() { registryRetryBackoff = prevBackoff })
	// Two 429s in a row, then a token: written out rather than derived from
	// registryRetryAttempts, so lowering the default fails here.
	calls := 0
	reg := newFakeRegistry(t, bearerChallenge, func(w http.ResponseWriter) {
		calls++
		if calls <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		tokenStatus(http.StatusOK)(w)
	})
	dockerHome(t, inlineDockerConfig(t, reg.host, "u", "p", ""))
	assert.NoError(t, probeRegistryCredential(context.Background(), reg.host, "", true))
	assert.Equal(t, 3, calls)
}

// A rejected NGC API key on a run that checks an installed stack is a
// warning: the cluster pulls with its own pull secret. Before install, and
// for any other credential, a rejection on a critical registry is an error.
func TestRegistryCredentialCheck_RejectedNGCKeyAfterInstall(t *testing.T) {
	rejectedKey := func(context.Context, string, string, bool) error {
		return registryProbeOutcome{kind: probeRejected, ngcKey: true, detail: "credentials from NGC_API_KEY rejected"}
	}
	rejectedLogin := func(context.Context, string, string, bool) error {
		return registryProbeOutcome{kind: probeRejected, detail: "credentials from docker config rejected"}
	}
	entry := RegistryEntry{Registry: "nvcr.io", Critical: true}
	for _, tc := range []struct {
		checker     RegistryCredentialChecker
		postInstall bool
		want        Severity
	}{
		{rejectedKey, true, SeverityWarning},
		{rejectedKey, false, SeverityError},
		{rejectedLogin, true, SeverityError},
	} {
		r := registryCredentialCheck(tc.checker, entry, "nvcr.io", tc.postInstall).Run(context.Background())
		assert.False(t, r.Passed)
		assert.Equal(t, tc.want, r.Severity, "postInstall=%v", tc.postInstall)
	}
}

// The stack's org is the probe's scope, so a key without access to it is
// caught even when the validator image comes from another org on the same
// registry: both scopes are probed, each on its own row.
func TestEnumerateRegistries_ProbesTheStacksOrg(t *testing.T) {
	stack := StackValues{Found: true, ImageRegistry: "nvcr.io", ImageRepository: "customerorg/team"}
	got := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:1.0.0", stack, nil)
	var scopes []string
	for _, e := range got {
		if e.Registry == "nvcr.io" {
			assert.True(t, e.Critical)
			scopes = append(scopes, e.RepoHint)
		}
	}
	assert.Equal(t, []string{"nvidia/nvcf-byoc/cluster-validator", "customerorg/team"}, scopes)

	cat := buildRegistryCredentialCategory(PreflightConfig{Registries: got,
		RegistryChecker: func(context.Context, string, string, bool) error { return nil }})
	var ids []string
	for _, c := range cat.checks {
		ids = append(ids, c.ID)
	}
	assert.Contains(t, ids, "registry-cred-nvcr.io")
	assert.Contains(t, ids, "registry-cred-nvcr.io/customerorg/team")

	mirrored := StackValues{Found: true, ImageRegistry: "harbor.example.com/nvcf", ImageRepository: "org/team"}
	got = EnumerateRegistries("", mirrored, nil)
	require.NotEmpty(t, got)
	assert.Equal(t, "nvcf/org/team", got[0].RepoHint, "the registry's path and the repository form the scope")

	cfg := buildControlPlaneValidatorConfig(EnumerateRegistries("nvcr.io/nvidia/cv:1", stack, nil))
	assert.Equal(t, 1, strings.Count(cfg, `host: "nvcr.io"`), "one registry is one in-pod endpoint")
}

// --cluster-validator-registries accepts host[:port][/path]; anything else is
// an error naming the entry rather than a row that silently never appears.
func TestParseRegistryExtra(t *testing.T) {
	for in, want := range map[string]RegistryEntry{
		"harbor.example.com":            {Registry: "harbor.example.com"},
		"harbor.example.com:443":        {Registry: "harbor.example.com"},
		"harbor.example.com:5000/nvcf":  {Registry: "harbor.example.com:5000", RepoHint: "nvcf"},
		"harbor.example.com/nvcf/team/": {Registry: "harbor.example.com", RepoHint: "nvcf/team"},
		"[fd00::1]":                     {Registry: "[fd00::1]"},
		"[fd00::1]:5000":                {Registry: "[fd00::1]:5000"},
	} {
		got, err := ParseRegistryExtra(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"https://harbor.example.com", "harbor.example.com:44x", "nvcr.io:", "nvcr.io:70000",
		"harbor.example.com//x", "nvcr.io@attacker.example", "harbor.example.com/a?b",
	} {
		_, err := ParseRegistryExtra(in)
		assert.ErrorContains(t, err, "expected host[:port]", in)
	}
}

// The gates read env values the way the stack does: the ingress release's
// condition is a YAML boolean, and the route gates are template ifs, which
// take a quoted "true" (and a quoted "false") as on. Each gate is toggled on
// its own so one cannot stand in for the other.
func TestLoadStackValues_GatewayGates(t *testing.T) {
	dir := t.TempDir()
	const gateways = `
    gateways:
      shared: {name: shared-gw, namespace: gw}
      grpc: {name: grpc-gw, namespace: gw}
      nats: {name: nats-gw, namespace: gw}
      llmGrpc: {name: llm-grpc-gw, namespace: gw}
      llmQuic: {name: llm-quic-gw, namespace: gw}
`
	base := []string{"gw/grpc-gw", "gw/shared-gw"}
	withNats := []string{"gw/grpc-gw", "gw/nats-gw", "gw/shared-gw"}
	withLLM := []string{"gw/grpc-gw", "gw/llm-grpc-gw", "gw/llm-quic-gw", "gw/shared-gw"}
	for name, tc := range map[string]struct {
		values string
		want   []string
	}{
		"nats only":          {"enabled: true\n    routes: {nats: {enabled: true}}", withNats},
		"llmWorker only":     {"enabled: true\n    routes: {llmWorker: {enabled: true}}", withLLM},
		"quoted nats true":   {"enabled: true\n    routes: {nats: {enabled: \"true\"}}", withNats},
		"quoted nats false":  {"enabled: true\n    routes: {nats: {enabled: \"false\"}}", withNats},
		"nats null":          {"enabled: true\n    routes: {nats: {enabled: null}}", base},
		"quoted gateway API": {"enabled: \"true\"", nil},
		"gateway API off":    {"enabled: false\n    routes: {nats: {enabled: true}}", nil},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".yaml")
		require.NoError(t, writeFile(path, []byte("ingress:\n  gatewayApi:\n    "+tc.values+gateways)), name)
		assert.Equal(t, tc.want, LoadStackValues([]string{path}).Gateways, name)
	}

	path := filepath.Join(dir, "slash.yaml")
	require.NoError(t, writeFile(path, []byte(
		"ingress:\n  gatewayApi:\n    enabled: true\n    gateways:\n"+
			"      shared: {name: a/b, namespace: gw}\n      grpc: {name: grpc-gw, namespace: gw/x}\n")))
	assert.Empty(t, LoadStackValues([]string{path}).Gateways, "a name or namespace with '/' is never forwarded")
}

// A dependency whose release condition is off is external to the stack, and
// one the values never mention is not reported.
func TestLoadStackValues_ExternalComponents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env.yaml")
	require.NoError(t, writeFile(path, []byte(
		"nats: {enabled: false}\nopenbao: {enabled: true}\ncassandra: {enabled: \"true\"}\n")))
	assert.Equal(t, []string{"cassandra", "nats"}, LoadStackValues([]string{path}).ExternalComponents)
	require.NoError(t, writeFile(path, []byte("global: {}\n")))
	assert.Empty(t, LoadStackValues([]string{path}).ExternalComponents)

	// An environment file layered over base.yaml turns components off.
	base, env := filepath.Join(t.TempDir(), "base.yaml"), filepath.Join(t.TempDir(), "prod.yaml")
	require.NoError(t, writeFile(base, []byte(
		"nats: {enabled: true}\nopenbao: {enabled: true}\ncassandra: {enabled: true}\n")))
	require.NoError(t, writeFile(env, []byte("openbao: {enabled: false}\ncassandra: {enabled: false}\n")))
	assert.Equal(t, []string{"cassandra", "openbao"}, LoadStackValues([]string{base, env}).ExternalComponents)
	assert.Empty(t, LoadStackValues([]string{base}).ExternalComponents)
}

// findSelfManagedStack returns the repository's self-managed stack, found
// above the package directory, or skips the test where it is not there, as
// in a sandboxed build.
func findSelfManagedStack(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		stack := filepath.Join(dir, "deploy", "stacks", "self-managed")
		if _, err := os.Stat(filepath.Join(stack, "helmfile.d")); err == nil {
			return stack
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("the self-managed stack is not available here")
		}
		dir = parent
	}
}

// The Gateways LoadStackValues forwards are the ones the stack renders into
// the ingress release's nvcfGatewayRoutes.gateways, for the default routes
// and with each gated route on, so the CLI's gates cannot drift from the
// template's. Needs helmfile; skipped without it.
func TestLoadStackValues_GatewaysMatchTheRenderedStack(t *testing.T) {
	if testing.Short() {
		t.Skip("renders the stack with helmfile")
	}
	helmfile, err := exec.LookPath("helmfile")
	if err != nil {
		t.Skip("helmfile is not installed")
	}
	stack := findSelfManagedStack(t)
	const common = `
ingress:
  gatewayApi:
    controllerNamespace: gateway
    gateways:
      shared: {name: shared-gw, namespace: gateway}
      grpc: {name: grpc-gw, namespace: gateway}
      nats: {name: nats-gw, namespace: gateway}
      llmGrpc: {name: llm-grpc-gw, namespace: llm}
      llmQuic: {name: llm-quic-gw, namespace: llm}
`
	const llmWorker = `
    routes:
      llmWorker: {enabled: true, backend: {namespace: nvcf}}
global:
  workerEndpoints: {llmRequestRouterAddress: "https://router.example.invalid:50071"}
addons:
  llm:
    requestRouter:
      backendRouter:
        pylonGrpcDialAddress: "https://router.example.invalid:50071"
        pylonReverseTunnelDialAddress: "router.example.invalid:50072"
      grpcTls: {enabled: true, issuerRef: {name: test-issuer}}
`
	for name, env := range map[string]string{
		"default routes":   common,
		"nats on":          common + "    routes:\n      nats: {enabled: true}\n",
		"quoted nats true": common + "    routes:\n      nats: {enabled: \"true\"}\n",
		"llmWorker on":     common + llmWorker,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			envFile := filepath.Join(dir, "env.yaml")
			require.NoError(t, writeFile(envFile, []byte(env)))
			out := filepath.Join(dir, "ingress-values.yaml")
			cmd := exec.Command(helmfile, "--file", "helmfile.d/02-core.yaml.gotmpl", "--environment", "default",
				"--state-values-file", envFile, "--selector", "name=ingress",
				"write-values", "--output-file-template", out)
			cmd.Dir = stack
			cmd.Env = append(os.Environ(), "HELMFILE_ENV=base", "HELMFILE_CACHE_HOME="+filepath.Join(dir, "cache"))
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			body, err := os.ReadFile(out)
			require.NoError(t, err)
			var rendered struct {
				NvcfGatewayRoutes struct {
					Gateways map[string]struct {
						Name      string `json:"name"`
						Namespace string `json:"namespace"`
					} `json:"gateways"`
				} `json:"nvcfGatewayRoutes"`
			}
			require.NoError(t, yaml.Unmarshal(body, &rendered))
			var want []string
			for _, gw := range rendered.NvcfGatewayRoutes.Gateways {
				want = append(want, gw.Namespace+"/"+gw.Name)
			}
			sort.Strings(want)
			require.NotEmpty(t, want, "the ingress release renders the shared and grpc Gateways")
			base := filepath.Join(stack, "environments", "base.yaml")
			assert.Equal(t, want, LoadStackValues([]string{base, envFile}).Gateways)
		})
	}
}

func TestParseExternalComponents(t *testing.T) {
	got, err := ParseExternalComponents([]string{" OpenBao ,nats", "nats", ""})
	require.NoError(t, err)
	assert.Equal(t, []string{"nats", "openbao"}, got)
	got, err = ParseExternalComponents(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	_, err = ParseExternalComponents([]string{"nats,redis"})
	require.ErrorContains(t, err, `unknown component "redis"`)
}
