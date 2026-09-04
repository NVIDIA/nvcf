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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -- probeRegistryCredential --

func TestProbeRegistryCredential_PublicRegistry(t *testing.T) {
	// Registry returns 200 on /v2/ → public, no credentials needed.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	// Registry returns 401 but the token endpoint returns 401 too → bad credentials.
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
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", "", nil)
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
	entries := EnumerateRegistries("nvcr.io/nvidia/nvcf-byoc/cluster-validator:3.1.0", "", nil)
	for _, e := range entries {
		if e.Registry == "nvcr.io" {
			assert.Equal(t, "nvidia/nvcf-byoc/cluster-validator", e.RepoHint,
				"RepoHint must carry the actual repo path for correct NGC token scope")
			return
		}
	}
	t.Fatal("nvcr.io not found in entries")
}

func TestEnumerateRegistries_AlwaysIncludesCertManager(t *testing.T) {
	// cert-manager always uses quay.io — it must appear even when not in extras.
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", "", nil)
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
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", "",
		[]string{"harbor.company.internal:443", "ghcr.io:443"})

	registries := make(map[string]bool, len(entries))
	for _, e := range entries {
		registries[e.Registry] = true
	}
	assert.True(t, registries["harbor.company.internal"], "extra registry must be added")
	assert.True(t, registries["ghcr.io"], "extra registry must be added")
}

func TestEnumerateRegistries_NoDuplicates(t *testing.T) {
	// Pass nvcr.io both as the image registry and as an extra — must not dedup.
	entries := EnumerateRegistries("nvcr.io/some/image:1.0", "",
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

	entries := EnumerateRegistries("nvcr.io/some/image:1.0", valuesPath, nil)
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
	got := readGlobalImageRegistry(path)
	assert.Equal(t, "nvcr.io", got)
}

func TestReadGlobalImageRegistry_MissingFile(t *testing.T) {
	got := readGlobalImageRegistry("/nonexistent/path/values.yaml")
	assert.Empty(t, got, "missing file must return empty string, not error")
}

func TestReadGlobalImageRegistry_MissingKey(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/values.yaml"
	require.NoError(t, writeFile(path, []byte(`other: value`)))
	got := readGlobalImageRegistry(path)
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
