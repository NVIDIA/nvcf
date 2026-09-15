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
	"os"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

const (
	registryProbeTimeout = 10 * time.Second

	// certManagerRegistry is cert-manager's upstream registry. It is only
	// contacted when the stack is not rewriting cert-manager images to
	// global.image.registry, which global.yaml.gotmpl does for all five of them.
	certManagerRegistry = "quay.io"

	// ngcRegistry is the default global.image.registry in the stack's
	// environments/base.yaml, and the fallback when no source named a
	// registry. See the tail of EnumerateRegistries.
	ngcRegistry = "nvcr.io"
)

// RegistryCredentialChecker probes whether credentials are present and valid
// for a registry. repoHint is the repository path used as the OAuth scope;
// pass "" to probe without a specific scope (works for public registries).
// critical signals that anonymous success is insufficient; configured credentials
// must be present. Returns nil on success, a descriptive error otherwise.
type RegistryCredentialChecker func(ctx context.Context, registry, repoHint string, critical bool) error

// NewRegistryCredentialChecker returns a production RegistryCredentialChecker
// backed by real HTTP calls.
func NewRegistryCredentialChecker() RegistryCredentialChecker {
	return probeRegistryCredential
}

// probeRegistryCredential authenticates to registry using the OCI Bearer token
// flow. repoHint is the OAuth scope repository path.
//
// When critical is true, anonymous success is not sufficient: the install pulls
// private repositories, so a local credential must also be present.
//
// Returns errRegistryProbeSkipped for registries this flow cannot speak to
// (ECR's SigV4, a Basic challenge, a 200 that does not look like a registry)
// and errRegistryCredentialsUnverified when no local credential is readable.
// Both are advisory: the caller must not fail the run on either.
func probeRegistryCredential(ctx context.Context, registry, repoHint string, critical bool) error {
	// ECR uses AWS SigV4, not the OCI Bearer flow, so this probe cannot speak
	// to it. Skip rather than fail: returning an error here makes an
	// ECR-hosted validator image unconditionally fail preflight, so --wait
	// could never converge.
	if isECRRegistry(registry) {
		return errRegistryProbeSkipped{
			reason: "ECR uses AWS SigV4; verify manually with 'aws ecr get-login-password'",
		}
	}

	pctx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()

	client := &http.Client{Timeout: registryProbeTimeout, CheckRedirect: refuseInsecureRedirect}

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

	// A 200 only proves /v2/ is anonymously readable, which says nothing about
	// the repositories the install actually pulls. Require the registry to
	// identify itself as an OCI registry so a captive portal or TLS-intercepting
	// proxy answering 200 HTML is not read as success.
	isOCI := resp.Header.Get("Docker-Distribution-Api-Version") != "" ||
		strings.Contains(resp.Header.Get("Www-Authenticate"), "realm")

	switch resp.StatusCode {
	case http.StatusOK:
		resp.Body.Close()
		if !isOCI {
			// A captive portal or TLS-intercepting proxy also answers 200. The
			// header is a Docker convention rather than an OCI requirement, so
			// a conformant registry may legitimately omit it: report that the
			// result is unverifiable instead of guessing either way.
			return errRegistryProbeSkipped{
				reason: fmt.Sprintf("%s answered 200 but did not identify as an OCI registry; "+
					"if this is unexpected, check for a proxy or captive portal", registry),
			}
		}
		// Anonymous read works. For a critical registry that is still not
		// enough: the install pulls private repositories, so fall through to
		// the credential requirement below rather than returning early.
		if !critical {
			return nil
		}
		return requireConfiguredCredentials(registry)
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

	// Only the Bearer flow is implemented. Self-hosted Harbor and htpasswd
	// mirrors commonly answer with Basic, where the Bearer path would report a
	// spurious credential failure, so skip instead.
	if scheme := authChallengeScheme(wwwAuth); scheme != "" && !strings.EqualFold(scheme, "Bearer") {
		return errRegistryProbeSkipped{
			reason: fmt.Sprintf("%s uses %s auth; only the OCI Bearer flow is probed", registry, scheme),
		}
	}

	_, err = exchangeBearerToken(pctx, client, registry, repoHint, wwwAuth)
	if err != nil {
		// Use credentialsForRegistry (not ngcCredentials) so NGC_API_KEY does
		// not masquerade as credentials for quay.io, GHCR, or Harbor — those
		// registries reject NGC tokens, which would wrongly produce "credentials
		// rejected" when the real diagnosis is "no credentials configured."
		if _, _, hasCreds := credentialsForRegistry(registry); !hasCreds {
			// Same situation as the critical path below, so the same verdict:
			// no readable local credential is not proof that none exists, since
			// a credential helper is invisible here.
			return errRegistryCredentialsUnverified{registry: registry}
		}
		return fmt.Errorf("credentials rejected by %s: %w", registry, err)
	}
	// For critical registries, a successful anonymous token is not enough:
	// if the actual install pulls private images, anonymous access will fail.
	if critical {
		return requireConfiguredCredentials(registry)
	}
	return nil
}

// errRegistryProbeSkipped marks a registry this probe cannot speak to. The
// caller reports it as a skip rather than a credential failure.
type errRegistryProbeSkipped struct{ reason string }

func (e errRegistryProbeSkipped) Error() string { return e.reason }

// authChallengeScheme returns the auth scheme named by a WWW-Authenticate
// header, or "" when the header is absent or malformed.
func authChallengeScheme(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if i := strings.IndexByte(header, ' '); i > 0 {
		return header[:i]
	}
	return header
}

// requireConfiguredCredentials reports an error when no credential is reachable
// for the registry.
//
// A missing credential is a warning, not a hard failure: credsFromDockerConfig
// reads only inline auth entries, so a workstation using a credential helper
// (credsStore on Docker Desktop, docker-credential-pass on Linux) has a working
// docker login that is invisible here. The install path also mints or mirrors a
// pull secret of its own, so preflight must not be the thing that blocks.
func requireConfiguredCredentials(registry string) error {
	if _, _, ok := credentialsForRegistry(registry); ok {
		return nil
	}
	return errRegistryCredentialsUnverified{registry: registry}
}

// errRegistryCredentialsUnverified means no credential was found locally. The
// caller downgrades this to a warning.
type errRegistryCredentialsUnverified struct{ registry string }

func (e errRegistryCredentialsUnverified) Error() string {
	return fmt.Sprintf("no local credentials found for %s "+
		"(a credential helper such as credsStore is not readable here); "+
		"if 'docker pull' works this can be ignored, otherwise add an entry to "+
		"~/.docker/config.json or set NGC_API_KEY for NGC registries", e.registry)
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

	add := func(registry, repoHint string, critical bool) {
		registry = strings.TrimSpace(registry)
		if registry == "" || seen[registry] {
			return
		}
		seen[registry] = true
		out = append(out, RegistryEntry{Registry: registry, RepoHint: repoHint, Critical: critical})
	}

	// Source 1: base registry from the configured validator image.
	// Carry the repo path as a scope hint so the token exchange uses the
	// operator's actual org rather than a fake one — NGC returns 403 for
	// orgs the API key cannot access, even if the key itself is valid.
	//
	// Critical follows the same rule as every other source rather than being
	// forced true: an air-gapped install that side-loaded the image never
	// contacts this registry at pull time (ImagePullPolicy is IfNotPresent).
	imageNamedRegistry := false
	if reg, repo, _, ok := parseImageRef(imageRef); ok && reg != "" {
		imageNamedRegistry = true
		add(reg, repo, isNGCRegistry(reg))
	}

	// Source 2: read global.image.registry from the environment values file.
	// This catches cases where the operator points at a custom NGC org or a
	// staging environment that differs from the validator image's registry.
	stackRegistry := ""
	if stackValuesFile != "" {
		stackRegistry = readGlobalImageRegistry(stackValuesFile)
		if stackRegistry != "" {
			// If it's an NGC registry, mark critical; customer mirrors are non-critical.
			add(stackRegistry, "", isNGCRegistry(stackRegistry))
		}
	}

	// Source 3: cert-manager, but only when the stack is not mirroring it.
	// deploy/stacks/self-managed/global.yaml.gotmpl rewrites every cert-manager
	// image to global.image.registry, so on a configured stack quay.io is never
	// contacted and probing it is a pointless round trip. It is only reachable
	// when no stack values file resolved.
	if stackRegistry == "" {
		add(certManagerRegistry, "", false)
	}

	// Source 4: operator-supplied extras (--cluster-validator-registries).
	// Preserve non-443 ports so probeRegistryCredential builds the correct
	// https://host:port/v2/ URL. net.JoinHostPort brackets IPv6 literals, which
	// a bare "%s:%d" would corrupt into https://::1:5000/v2/.
	for _, e := range extras {
		host, port := parseRegistryHostPort(e)
		if host == "" {
			continue
		}
		reg := host
		if port != 0 && port != 443 {
			reg = net.JoinHostPort(host, strconv.Itoa(port))
		} else if strings.Contains(host, ":") {
			reg = "[" + host + "]" // bare IPv6 literal on the default port
		}
		add(reg, "", false)
	}

	// Fallback for "no source named a registry at all": no validator image is
	// configured and no stack values file resolved. That is the default
	// configuration rather than an edge case, and without this the run reports
	// on quay.io alone while the operator's NGC credentials go unchecked.
	// environments/base.yaml sets global.image.registry to nvcr.io, so it is
	// the right guess when there is nothing else to go on.
	//
	// Deliberately not triggered when the image or the stack named a non-NGC
	// registry: that is a mirrored install, which has no reason to reach
	// nvcr.io, and probing it there would add a failing round trip for the
	// sites least able to make it.
	//
	// Non-critical, unlike an NGC registry a source actually named. A guess
	// must not hard-fail the run, and this category has no opt-out flag.
	if !imageNamedRegistry && stackRegistry == "" {
		add(ngcRegistry, "", false)
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
