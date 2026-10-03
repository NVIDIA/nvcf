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
	"sort"
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
		strings.Contains(strings.Join(resp.Header.Values("Www-Authenticate"), ","), "realm")

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
		// enough: the install pulls private repositories, so a credential must
		// exist. It cannot be verified here, since /v2/ accepts anyone, so say
		// so rather than report "credentials valid".
		if !critical {
			return nil
		}
		if err := requireConfiguredCredentials(registry); err != nil {
			return err
		}
		return errRegistryCredentialsNotVerified{registry: registry}
	case http.StatusUnauthorized:
		// Auth required - proceed with token exchange.
	default:
		resp.Body.Close()
		return fmt.Errorf("unexpected status %s from %s", resp.Status, registry)
	}

	// Step 2: exchange credentials for a Bearer token using the actual repo
	// from the configured image (repoHint). Using a fake repo name causes
	// org-level 403s from NGC and GHCR for non-existent orgs, which is
	// indistinguishable from bad credentials.
	wwwAuth := selectAuthChallenge(resp.Header.Values("Www-Authenticate"))
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
		// not masquerade as credentials for quay.io, GHCR, or Harbor - those
		// registries reject NGC tokens, which would wrongly produce "credentials
		// rejected" when the real diagnosis is "no credentials configured."
		if _, _, hasCreds := credentialsForRegistry(registry); !hasCreds {
			// Same situation as the critical path below, so the same verdict:
			// no readable local credential is not proof that none exists (a
			// helper binary may be missing from PATH, or the login may live
			// outside the docker config entirely).
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

// errRegistryCredentialsNotVerified marks a registry that allows anonymous
// access to /v2/: a local credential exists, but nothing here proves the
// registry accepts it. Reported as a pass that says so.
type errRegistryCredentialsNotVerified struct{ registry string }

func (e errRegistryCredentialsNotVerified) Error() string {
	return "credentials configured; not verified, because the registry allows anonymous access to /v2/"
}

// errRegistryProbeSkipped marks a registry this probe cannot speak to. The
// caller reports it as a skip rather than a credential failure.
type errRegistryProbeSkipped struct{ reason string }

func (e errRegistryProbeSkipped) Error() string { return e.reason }

// selectAuthChallenge picks the challenge to answer when a registry offers
// several, as it may in separate WWW-Authenticate headers: the Bearer one if
// present, otherwise the first. Reading only the first header reported a
// registry answering "Negotiate" then "Bearer" as a clean skip.
func selectAuthChallenge(challenges []string) string {
	for _, c := range challenges {
		if strings.EqualFold(authChallengeScheme(c), "Bearer") {
			return c
		}
		// Several challenges can also share one header, comma-separated:
		// `Negotiate, Bearer realm="..."`. Take the Bearer one from there.
		if i := bearerChallengeStart(c); i > 0 {
			return c[i:]
		}
	}
	if len(challenges) > 0 {
		return challenges[0]
	}
	return ""
}

// bearerChallengeStart returns where a Bearer challenge begins after a comma
// in a joined WWW-Authenticate value, or -1.
func bearerChallengeStart(header string) int {
	lower := strings.ToLower(header)
	for from := 0; ; {
		j := strings.Index(lower[from:], "bearer ")
		if j < 0 {
			return -1
		}
		j += from
		if j > 0 && strings.HasSuffix(strings.TrimRight(lower[:j], " "), ",") {
			return j
		}
		from = j + 1
	}
}

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
// reads inline auth entries and asks credsStore / credHelpers helpers, but a
// helper binary can be absent from PATH or refuse to answer non-interactively.
// The install path also mints or mirrors a pull secret of its own, so preflight
// must not be the thing that blocks.
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

// StackValues are the stack settings preflight needs, read from the stack's
// environment values with the same layering helmfile uses.
type StackValues struct {
	// Found is true when at least one values file was read.
	Found bool
	// ImageRegistry is global.image.registry. The stack renders
	// registry + "/" + repository, so it may carry a path.
	ImageRegistry string
	// ACMESolverRepository is certManager.acmesolver.image.repository. When
	// unset the ACME solver keeps the chart's quay.io/jetstack default.
	ACMESolverRepository string
	// EnvoyGatewayNamespace is ingress.gatewayApi.controllerNamespace.
	EnvoyGatewayNamespace string
	// Gateways are the NVCF Gateways the stack names under
	// ingress.gatewayApi.gateways, as sorted, distinct namespace/name entries.
	Gateways []string
}

// LoadStackValues reads files in order and layers each over the previous,
// the way helmfile layers an environment file over base.yaml. Unreadable or
// malformed files are skipped.
func LoadStackValues(files []string) StackValues {
	merged := map[string]any{}
	found := false
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			continue
		}
		found = true
		mergeValues(merged, doc)
	}
	values := StackValues{
		Found:                 found,
		ImageRegistry:         digString(merged, "global", "image", "registry"),
		ACMESolverRepository:  digString(merged, "certManager", "acmesolver", "image", "repository"),
		EnvoyGatewayNamespace: digString(merged, "ingress", "gatewayApi", "controllerNamespace"),
		Gateways:              stackGateways(merged),
	}
	return values
}

// stackGateways returns the Gateways the stack wires NVCF routes to, as
// sorted, distinct namespace/name entries, applying the gates its template
// applies (global.yaml.gotmpl, nvcfGatewayRoutes): none unless
// ingress.gatewayApi.enabled, shared and grpc always, nats only with its
// route enabled, and llmGrpc and llmQuic only with the LLM worker route. An
// environment file can name a Gateway whose route stays off, and the stack
// never creates or wires that Gateway, so the validator must not look for it.
func stackGateways(merged map[string]any) []string {
	if !digBool(merged, "ingress", "gatewayApi", "enabled") {
		return nil
	}
	wired := []string{"shared", "grpc"}
	if digBool(merged, "ingress", "gatewayApi", "routes", "nats", "enabled") {
		wired = append(wired, "nats")
	}
	if digBool(merged, "ingress", "gatewayApi", "routes", "llmWorker", "enabled") {
		wired = append(wired, "llmGrpc", "llmQuic")
	}
	gateways, _ := digAny(merged, "ingress", "gatewayApi", "gateways").(map[string]any)
	seen := map[string]bool{}
	var out []string
	for _, key := range wired {
		name := digString(gateways, key, "name")
		ns := digString(gateways, key, "namespace")
		if name == "" || ns == "" || strings.Contains(name, "/") || strings.Contains(ns, "/") {
			continue
		}
		if entry := ns + "/" + name; !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return out
}

// mergeValues deep-merges src into dst: maps merge key by key, anything else
// in src replaces what dst held.
func mergeValues(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				mergeValues(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

// digBool reads a YAML boolean, false when absent or of another type, as the
// stack's dig with a false default reads it.
func digBool(m map[string]any, keys ...string) bool {
	b, _ := digAny(m, keys...).(bool)
	return b
}

func digString(m map[string]any, keys ...string) string {
	s, _ := digAny(m, keys...).(string)
	return strings.TrimSpace(s)
}

func digAny(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// EnumerateRegistries builds the deduplicated list of registries to credential-
// check from the image ref, cert-manager (quay.io), the stack values
// (global.image.registry), and operator-supplied extras. stackValuesFiles are
// layered in order (base.yaml first, then the environment file).
func EnumerateRegistries(imageRef string, stackValuesFiles []string, extras []string) []RegistryEntry {
	seen := make(map[string]bool)
	var out []RegistryEntry
	stack := LoadStackValues(stackValuesFiles)

	// add reports whether the registry is in the list, so a source only counts
	// as having named a registry once its value passed validation.
	add := func(registry, repoHint string, critical bool) bool {
		registry = strings.TrimSpace(registry)
		if registry == "" {
			return false
		}
		if seen[registry] {
			return true
		}
		// Every source goes through the same host validation. The probe builds
		// "https://" + registry + "/v2/", so a value carrying "/" or "@" moves
		// the host: global.image.registry set to "nvcr.io@attacker.example.com"
		// in a values file the CLI finds by walking up from CWD would otherwise
		// aim an outbound request wherever that names. Extras were guarded via
		// parseRegistryHostPort; the image ref and the values file were not.
		if !isBareRegistryHost(strings.TrimSuffix(strings.TrimPrefix(registry, "["), "]")) &&
			!isBareRegistryHost(registry) {
			return false
		}
		seen[registry] = true
		out = append(out, RegistryEntry{Registry: registry, RepoHint: repoHint, Critical: critical})
		return true
	}

	// Source 1: base registry from the configured validator image.
	// Carry the repo path as a scope hint so the token exchange uses the
	// operator's actual org rather than a fake one - NGC returns 403 for
	// orgs the API key cannot access, even if the key itself is valid.
	//
	// Critical follows the same rule as every other source rather than being
	// forced true: an air-gapped install that side-loaded the image never
	// contacts this registry at pull time (ImagePullPolicy is IfNotPresent).
	imageNamedRegistry := false
	if reg, repo, _, ok := parseImageRef(imageRef); ok && reg != "" {
		imageNamedRegistry = add(reg, repo, isNGCRegistry(reg))
	}

	// Source 2: global.image.registry from the stack values. This catches an
	// operator pointing at a custom NGC org or a staging registry that differs
	// from the validator image's. The stack renders registry + "/" + repository,
	// so "harbor.corp.example/nvcf" is a valid value: probe its host and carry
	// the path as the scope hint rather than rejecting the whole value.
	stackNamedRegistry := false
	if host, path, _ := strings.Cut(stack.ImageRegistry, "/"); host != "" {
		// If it's an NGC registry, mark critical; customer mirrors are non-critical.
		stackNamedRegistry = add(host, path, isNGCRegistry(host))
	}

	// Source 3: cert-manager's ACME solver. global.yaml.gotmpl moves the
	// controller images to the mirror, but the solver keeps the chart's
	// quay.io/jetstack default unless certManager.acmesolver.image is set, so
	// a mirrored site still pulls it from quay.io at its first HTTP-01 order.
	// Probe quay.io unless the stack points the solver somewhere else.
	if acme := stack.ACMESolverRepository; !stack.Found || acme == "" ||
		strings.HasPrefix(acme, certManagerRegistry+"/") {
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
			// IPv6 literal on the default port. parseRegistryHostPort returns
			// it unbracketed, so bracket it once for the URL host.
			reg = "[" + host + "]"
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
	if !imageNamedRegistry && !stackNamedRegistry {
		add(ngcRegistry, "", false)
	}

	return out
}
