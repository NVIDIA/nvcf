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
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

const (
	registryProbeTimeout = 10 * time.Second

	// certManagerRegistry is the well-known exception from the helmfile:
	// cert-manager uses quay.io/jetstack images, not global.image.registry.
	certManagerRegistry = "quay.io"
)

// RegistryCredentialChecker probes whether credentials are present and valid
// for a registry. repoHint is the repository path used as the OAuth scope;
// pass "" to probe without a specific scope (works for public registries).
// Returns nil on success, a descriptive error otherwise.
type RegistryCredentialChecker func(ctx context.Context, registry, repoHint string) error

// NewRegistryCredentialChecker returns a production RegistryCredentialChecker
// backed by real HTTP calls.
func NewRegistryCredentialChecker() RegistryCredentialChecker {
	return probeRegistryCredential
}

// probeRegistryCredential authenticates to registry using the OCI Bearer token
// flow. repoHint is the OAuth scope repository path. ECR registries return a
// clear diagnostic instead of attempting the Bearer flow.
func probeRegistryCredential(ctx context.Context, registry, repoHint string) error {
	if isECRRegistry(registry) {
		return fmt.Errorf("ECR registry detected — credential validation requires AWS CLI; " +
			"run 'aws ecr get-login-password' to verify manually")
	}

	pctx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()

	client := &http.Client{Timeout: registryProbeTimeout}

	// Step 1: probe /v2/ unauthenticated.
	probeURL := "https://" + registry + "/v2/"
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", registry, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// Public registry — no credentials needed.
		resp.Body.Close()
		return nil
	case http.StatusUnauthorized:
		// Auth required — proceed with token exchange.
	default:
		resp.Body.Close()
		return fmt.Errorf("unexpected status %s from %s", resp.Status, registry)
	}

	// Step 2: exchange credentials for a Bearer token using the actual repo
	// from the configured image (repoHint). Using a fake repo name causes
	// org-level 403s from NGC and GHCR for non-existent orgs, which is
	// indistinguishable from bad credentials.
	wwwAuth := resp.Header.Get("Www-Authenticate")
	resp.Body.Close()

	_, err = exchangeBearerToken(pctx, client, registry, repoHint, wwwAuth)
	if err != nil {
		// Use credentialsForRegistry (not ngcCredentials) so NGC_API_KEY does
		// not masquerade as credentials for quay.io, GHCR, or Harbor — those
		// registries reject NGC tokens, which would wrongly produce "credentials
		// rejected" when the real diagnosis is "no credentials configured."
		_, _, hasCreds := credentialsForRegistry(registry)
		if !hasCreds {
			return fmt.Errorf("no credentials configured for %s "+
				"(add to ~/.docker/config.json or set NGC_API_KEY for NGC registries)", registry)
		}
		return fmt.Errorf("credentials rejected by %s: %w", registry, err)
	}
	return nil
}

// isECRRegistry returns true for AWS Elastic Container Registry hostnames,
// which use AWS SigV4 auth instead of the OCI Bearer token flow.
func isECRRegistry(registry string) bool {
	return strings.Contains(registry, ".dkr.ecr.") &&
		strings.HasSuffix(registry, ".amazonaws.com")
}

// RegistryEntry is one registry endpoint to credential-check.
type RegistryEntry struct {
	// Registry is the hostname (and optional port) of the container registry.
	Registry string
	// RepoHint is the repository path used as the OAuth scope when probing
	// credentials (e.g. "nvidia/nvcf-byoc/cluster-validator" for NGC).
	// Empty means probe without a specific scope, which works for public
	// registries (quay.io, Docker Hub public images) and GHCR anonymous access.
	RepoHint string
	// Critical marks registries whose credential failure should be a hard error
	// rather than a warning. NGC (nvcr.io) is always critical; customer-supplied
	// extras default to non-critical.
	Critical bool
}

// EnumerateRegistries builds the deduplicated list of registries to credential-
// check from the image ref, cert-manager (quay.io), the stack values file
// (global.image.registry), and operator-supplied extras.
func EnumerateRegistries(imageRef, stackValuesFile string, extras []string) []RegistryEntry {
	seen := make(map[string]bool)
	var out []RegistryEntry

	add := func(registry string, critical bool) {
		registry = strings.TrimSpace(registry)
		if registry == "" || seen[registry] {
			return
		}
		seen[registry] = true
		out = append(out, RegistryEntry{Registry: registry, Critical: critical})
	}

	// Source 1: base registry from the configured validator image.
	// Carry the repo path as a scope hint so the token exchange uses the
	// operator's actual org rather than a fake one — NGC returns 403 for
	// orgs the API key cannot access, even if the key itself is valid.
	if reg, repo, _, ok := parseImageRef(imageRef); ok && reg != "" {
		seen[reg] = true
		out = append(out, RegistryEntry{Registry: reg, RepoHint: repo, Critical: true})
	}

	// Source 2: read global.image.registry from the environment values file.
	// This catches cases where the operator points at a custom NGC org or a
	// staging environment that differs from the validator image's registry.
	if stackValuesFile != "" {
		if reg := readGlobalImageRegistry(stackValuesFile); reg != "" {
			// If it's an NGC registry, mark critical; customer mirrors are non-critical.
			add(reg, isNGCRegistry(reg))
		}
	}

	// Source 3: cert-manager's well-known exception (quay.io/jetstack).
	// cert-manager ignores global.image.registry and always pulls from quay.io.
	add(certManagerRegistry, false)

	// Source 4: operator-supplied extras (--cluster-validator-registries).
	for _, e := range extras {
		host, _ := parseRegistryHostPort(e)
		if host != "" {
			add(host, false)
		}
	}

	return out
}

// readGlobalImageRegistry reads the global.image.registry key from an
// environment values YAML file (e.g. environments/local.yaml). Returns ""
// on any error so the caller can safely ignore missing or malformed files.
func readGlobalImageRegistry(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var vals struct {
		Global struct {
			Image struct {
				Registry string `json:"registry" yaml:"registry"`
			} `json:"image" yaml:"image"`
		} `json:"global" yaml:"global"`
	}
	if err := yaml.Unmarshal(data, &vals); err != nil {
		return ""
	}
	return strings.TrimSpace(vals.Global.Image.Registry)
}
