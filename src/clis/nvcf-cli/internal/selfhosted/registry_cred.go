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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

const (
	registryProbeTimeout = 10 * time.Second

	// certManagerRegistry is cert-manager's upstream registry. The stack moves
	// the cert-manager controller images to global.image.registry, but the
	// ACME solver stays here unless certManager.acmesolver.image is set.
	certManagerRegistry = "quay.io"

	// ngcRegistry is the default global.image.registry in the stack's
	// environments/base.yaml, and the fallback when no source named a
	// registry. See the tail of EnumerateRegistries.
	ngcRegistry = "nvcr.io"
)

// RegistryCredentialChecker probes whether the local credential for a
// registry is accepted. repoHint is the repository path used as the OAuth
// scope; pass "" to probe without a specific scope. critical signals that
// anonymous access is not enough: the install pulls private repositories.
// chartScope marks, before install, the scope helm on this machine pulls the
// stack's charts from with the docker login, which the probe then judges for
// it even where the run sends the NGC key first.
// Returns nil when a credential was sent and accepted, and otherwise a
// registryProbeOutcome or a plain error for a probe the run's context ended.
type RegistryCredentialChecker func(ctx context.Context, registry, repoHint string, critical, chartScope bool) error

// NewRegistryCredentialChecker returns a production RegistryCredentialChecker
// backed by real HTTP calls.
func NewRegistryCredentialChecker() RegistryCredentialChecker {
	return probeRegistryCredential
}

// registryProbeKind classifies a probe that did not verify a credential.
type registryProbeKind int

const (
	// probeSkipped: the registry speaks a flow this probe does not (ECR's
	// SigV4, Basic auth, no Bearer realm), did not identify as a registry,
	// or named a token realm the CLI will not send credentials to.
	probeSkipped registryProbeKind = iota + 1
	// probeAnonymous: reachable without a credential, and none was found
	// locally, so nothing was checked.
	probeAnonymous
	// probeNotVerified: a credential exists, but /v2/ lets anyone in, so it
	// was never sent.
	probeNotVerified
	// probeNoCredential: the registry needs a credential and none was read
	// locally. Not proof there is none: a helper may not be readable here.
	probeNoCredential
	// probeUnverifiable: the registry or its token service could not be
	// reached, or failed, after retries. A limit of this machine or a
	// transient fault, not evidence about the credential.
	probeUnverifiable
	// probeRejected: the token endpoint answered 401 or 403 to a request
	// that carried the credential. The one outcome that is evidence of a bad
	// credential.
	probeRejected
	// probeLoginRejected: the registry rejected the docker login and
	// accepted the NGC key sent in its place. The run uses the key, but
	// docker and helm on this machine still send the login, so it fails like
	// a rejected credential.
	probeLoginRejected
	// probeOtherCredential: the docker login has no access to the scope, and
	// the registry accepted the NGC key for it, which the run uses there.
	// Before install it fails for the scope helm pulls the stack's charts
	// from with the docker login.
	probeOtherCredential
)

// registryProbeOutcome is the result of a probe that did not verify a
// credential, or verified one only after the registry rejected another or
// gave it no access. Only probeRejected, probeLoginRejected, and before
// install probeOtherCredential for the stack's chart scope, can fail a run.
type registryProbeOutcome struct {
	kind   registryProbeKind
	detail string
	// refused is the credential a probeRejected or probeLoginRejected is
	// about: the one the registry refused, or the docker login it rejected
	// before it accepted the NGC key. A run that checks an installed stack
	// looks for it in the cluster's pull secrets.
	refused registryCredential
	// keyOnly marks a probeRejected of the NGC key with no docker login
	// rejected for the scope: the one refusal that may be this machine's
	// alone once the stack is installed.
	keyOnly bool
}

func (o registryProbeOutcome) Error() string { return o.detail }

// probeRegistryCredential authenticates to registry using the OCI Bearer token
// flow, with the run's local credential for it. repoHint is the OAuth scope
// repository path.
//
// Exactly one result can count against the credential: a 401 or 403 from the
// token endpoint to a request that carried it. Unreachable registries, token
// services that fail or throttle, and realms the CLI refuses are retried where
// that can help, then reported as advisory outcomes.
func probeRegistryCredential(ctx context.Context, registry, repoHint string, critical, chartScope bool) error {
	// ECR uses AWS SigV4, not the OCI Bearer flow, so this probe cannot speak
	// to it.
	if isECRRegistry(registry) {
		return registryProbeOutcome{kind: probeSkipped,
			detail: "ECR uses AWS SigV4; verify with: " + ecrLoginHint(registry)}
	}

	pctx, cancel := context.WithTimeout(ctx, registryProbeTimeout)
	defer cancel()
	client := newRegistryHTTPClient(registryProbeTimeout)
	creds := registryCredentialsFrom(ctx)

	// interrupted keeps an ended run from being graded as a registry fault.
	interrupted := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("checking %s: %w", registry, ctx.Err())
		}
		return err
	}

	// Step 1: probe /v2/ unauthenticated.
	resp, err := doRegistryRequest(pctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(pctx, http.MethodGet, "https://"+registry+"/v2/", nil)
	})
	if err != nil {
		var refused errRefusedRedirect
		if errors.As(err, &refused) {
			return registryProbeOutcome{kind: probeSkipped, detail: refused.msg}
		}
		return interrupted(registryProbeOutcome{kind: probeUnverifiable,
			detail: fmt.Sprintf("could not verify credentials from this machine: cannot reach %s: %v", registry, err)})
	}

	// A response only says something about credentials when it comes from a
	// registry. A captive portal or TLS-intercepting proxy answers too, so
	// require the registry to identify itself. The header is a Docker
	// convention rather than an OCI requirement, so a conformant registry
	// may omit it: report that the result is unverifiable instead of
	// guessing either way.
	isOCI := resp.Header.Get("Docker-Distribution-Api-Version") != "" ||
		strings.Contains(strings.Join(resp.Header.Values("Www-Authenticate"), ","), "realm")
	wwwAuth := selectAuthChallenge(resp.Header.Values("Www-Authenticate"))
	resp.Body.Close()
	switch {
	case isTransientStatus(resp.StatusCode) || (isOCI && resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusUnauthorized):
		return interrupted(registryProbeOutcome{kind: probeUnverifiable,
			detail: fmt.Sprintf("could not verify credentials from this machine: %s answered %s", registry, resp.Status)})
	case !isOCI && resp.StatusCode != http.StatusUnauthorized:
		return registryProbeOutcome{kind: probeSkipped,
			detail: fmt.Sprintf("%s answered %s but did not identify as an OCI registry; "+
				"if this is unexpected, check for a proxy or captive portal", registry, resp.Status)}
	}

	if resp.StatusCode == http.StatusOK {
		// Anonymous read works, which says nothing about the repositories
		// the install pulls, and a credential cannot be checked where /v2/
		// accepts anyone.
		read := creds.read(pctx, registry)
		if read.ok {
			return registryProbeOutcome{kind: probeNotVerified,
				detail: "credentials configured (" + read.cred.source + "); not verified, " +
					"because the registry allows anonymous access to /v2/"}
		}
		return anonymousOutcome(registry, critical, read.err)
	}

	// Only the Bearer flow is implemented. Self-hosted Harbor and htpasswd
	// mirrors commonly answer with Basic, where the Bearer path would report a
	// spurious credential failure, so skip instead.
	if scheme := authChallengeScheme(wwwAuth); scheme != "" && !strings.EqualFold(scheme, "Bearer") {
		return registryProbeOutcome{kind: probeSkipped,
			detail: fmt.Sprintf("%s uses %s auth; only the OCI Bearer flow is probed", registry, scheme)}
	}

	// Step 2: exchange the credential the run settled for this scope for a
	// Bearer token scoped to the actual repository (repoHint). A made-up
	// repository name draws org-level 403s from NGC and GHCR.
	settled := creds.lookupChallenged(pctx, registry, repoHint, wwwAuth)
	if chartScope {
		settled = creds.judgeLogin(pctx, registry, repoHint, wwwAuth, settled)
	}
	cred, hasCred, lookupErr := settled.cred, settled.ok, settled.err
	var sent *registryCredential
	if hasCred {
		sent = &cred
	}
	_, err = exchangeBearerToken(pctx, client, registry, repoHint, wwwAuth, sent)
	note := ""
	if login := settled.rejectedLogin; login != nil && hasCred {
		note = "; " + rejectedLoginNote(registry, login.source)
	}
	if err == nil {
		switch {
		case !hasCred:
			return anonymousOutcome(registry, critical, lookupErr)
		case note != "":
			return registryProbeOutcome{kind: probeLoginRejected, refused: *settled.rejectedLogin,
				detail: note[2:] + "; credentials from " + cred.source + " valid, and the run uses them in its place"}
		case settled.noAccessLogin != nil:
			return registryProbeOutcome{kind: probeOtherCredential, detail: fmt.Sprintf(
				"credentials from %s valid; the docker login from %s has no access to %s, so the run sends %s for it",
				cred.source, settled.noAccessLogin.source, scopeLabel(registry, repoHint), cred.source)}
		case settled.loginUnjudged != "":
			return registryProbeOutcome{kind: probeUnverifiable, detail: fmt.Sprintf(
				"credentials from %s valid; could not verify the docker login helm on this machine pulls the "+
					"charts from %s with: %s", cred.source, scopeLabel(registry, repoHint), settled.loginUnjudged)}
		}
		return nil
	}
	var te *tokenExchangeError
	switch {
	case !errors.As(err, &te):
		return interrupted(registryProbeOutcome{kind: probeUnverifiable,
			detail: "could not verify credentials from this machine: " + err.Error() + note})
	case te.refused:
		return registryProbeOutcome{kind: probeSkipped, detail: te.msg}
	case te.rejected() && te.status == http.StatusForbidden:
		// A 403 is no access to this repository, which says nothing about
		// whether the credential itself is still valid.
		detail := fmt.Sprintf("credentials from %s have no access to %s: %s; use a credential with pull access to it",
			cred.source, scopeLabel(registry, repoHint), te.msg)
		if settled.fallbackFailure != "" {
			detail += "; " + settled.fallbackFailure
		}
		return registryProbeOutcome{kind: probeRejected, detail: detail + note, refused: cred,
			keyOnly: cred.ngcKey && settled.rejectedLogin == nil}
	case te.rejected():
		return registryProbeOutcome{kind: probeRejected, detail: fmt.Sprintf("credentials from %s rejected: %s; %s%s",
			cred.source, te.msg, rejectedHint(registry, cred), note), refused: cred,
			keyOnly: cred.ngcKey && settled.rejectedLogin == nil}
	case !hasCred && (te.status == http.StatusUnauthorized || te.status == http.StatusForbidden):
		// Anonymous access refused: the registry needs a credential this
		// machine does not have.
		return noCredentialOutcome(registry, lookupErr)
	}
	return interrupted(registryProbeOutcome{kind: probeUnverifiable,
		detail: "could not verify credentials from this machine: " + te.msg + note})
}

// scopeLabel names the repository a probe asked for, or the registry when it
// asked for none.
func scopeLabel(registry, repo string) string {
	if repo == "" {
		return registry
	}
	return registry + "/" + repo
}

// rejectedLoginNote says that registry rejected the docker login read from
// source, which docker and helm on this machine still send, and how to renew
// it.
func rejectedLoginNote(registry, source string) string {
	return fmt.Sprintf("the docker login from %s was rejected, and docker and helm on this machine still send it; "+
		"renew it with: docker login %s --username '$oauthtoken'", source, registry)
}

// anonymousOutcome grades a registry that let this machine in without a
// credential. A critical registry still needs one for the private
// repositories the install pulls.
func anonymousOutcome(registry string, critical bool, lookupErr error) error {
	if critical || lookupErr != nil {
		return noCredentialOutcome(registry, lookupErr)
	}
	return registryProbeOutcome{kind: probeAnonymous, detail: "reachable anonymously; no local credential checked"}
}

// noCredentialOutcome reports that no local credential was read for
// registry, with the reason a credential store failed when one did.
func noCredentialOutcome(registry string, lookupErr error) error {
	why := "(a credential helper such as credsStore may not be readable here)"
	if lookupErr != nil {
		why = "(reading them failed: " + lookupErr.Error() + ")"
	}
	return registryProbeOutcome{kind: probeNoCredential,
		detail: fmt.Sprintf("no local credentials found for %s %s; if 'docker pull' works this can be ignored, "+
			"otherwise %s", registry, why, loginHint(registry))}
}

// loginHint says how to give this machine a credential for registry. docker
// reads $DOCKER_CONFIG/config.json, ~/.docker/config.json by default.
func loginHint(registry string) string {
	if isECRRegistry(registry) {
		return "run: " + ecrLoginHint(registry)
	}
	login := "run: docker login " + registry
	if isNGCKeyRegistry(registry) {
		return "export NGC_API_KEY, or " + login + " --username '$oauthtoken'"
	}
	return login + " (docker reads $DOCKER_CONFIG/config.json, ~/.docker/config.json by default)"
}

// rejectedHint says how to replace a credential registry refused.
func rejectedHint(registry string, cred registryCredential) string {
	if cred.ngcKey {
		return "generate a new NGC API key and export it as " + cred.source
	}
	return loginHint(registry)
}

// ecrLoginHint is a command that logs in to an ECR registry in its own
// region, which also proves the registry accepts the operator's AWS identity.
func ecrLoginHint(registry string) string {
	region := ""
	if host, _, err := net.SplitHostPort(registry); err == nil {
		registry = host
	}
	if _, rest, ok := strings.Cut(registry, ".dkr.ecr."); ok {
		region, _, _ = strings.Cut(rest, ".")
	}
	cmd := "aws ecr get-login-password"
	if region != "" {
		cmd += " --region " + region
	}
	return cmd + " | docker login --username AWS --password-stdin " + registry
}

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

// isECRRegistry returns true for AWS Elastic Container Registry hostnames,
// which use AWS SigV4 auth instead of the OCI Bearer token flow.
func isECRRegistry(registry string) bool {
	if host, _, err := net.SplitHostPort(registry); err == nil {
		registry = host
	}
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
	// Critical marks a registry a source named that the install must pull
	// private images from: an NVIDIA registry named by the validator image or
	// the stack. A credential it rejects fails the run; everything else about
	// it, and every other registry, is a warning at most.
	Critical bool
	// Charts marks the scope helm on this machine pulls the stack's charts
	// from, with the docker login: the stack's OCI chart source.
	Charts bool
	// ChartsOnly marks a Charts entry no other source named. The clusters
	// pull nothing from it, so the control-plane validator does not dial it.
	ChartsOnly bool
}

// ParseRegistryExtra parses one --cluster-validator-registries entry,
// host[:port][/path]. A path, as the stack's global.image.registry may carry,
// becomes the probe's scope hint. Anything else is an error naming the entry.
func ParseRegistryExtra(s string) (RegistryEntry, error) {
	s = strings.TrimSpace(s)
	hostPort, path, _ := strings.Cut(s, "/")
	host, port := parseRegistryHostPort(hostPort)
	path = strings.TrimSuffix(path, "/")
	if host == "" || (path != "" && !isRepositoryPath(path)) {
		return RegistryEntry{}, fmt.Errorf("%q: expected host[:port] or host[:port]/path, "+
			"for example harbor.example.com:5000/nvcf", s)
	}
	reg := host
	if port != 443 {
		reg = net.JoinHostPort(host, strconv.Itoa(port))
	} else if strings.Contains(host, ":") {
		// IPv6 literal on the default port. parseRegistryHostPort returns
		// it unbracketed, so bracket it once for the URL host.
		reg = "[" + host + "]"
	}
	return RegistryEntry{Registry: reg, RepoHint: path}, nil
}

// StackValues are the stack settings preflight needs, read from the stack's
// environment values with the same layering helmfile uses.
type StackValues struct {
	// Found is true when at least one values file was read.
	Found bool
	// ImageRegistry is global.image.registry. The stack renders
	// registry + "/" + repository, so it may carry a path.
	ImageRegistry string
	// ImageRepository is global.image.repository, the NGC org and team or
	// mirror path every stack image is pulled from.
	ImageRepository string
	// ChartRegistry and ChartRepository are global.helm.sources.registry and
	// .repository, the OCI repository helm pulls the stack's charts from with
	// the docker login. Both are empty when global.helm.sources.url names an
	// HTTPS chart repository instead, which has credentials of its own.
	ChartRegistry, ChartRepository string
	// CertManagerEnabled is certManager.enabled, the cert-manager release's
	// condition.
	CertManagerEnabled bool
	// ACMESolverRepository is certManager.acmesolver.image.repository. When
	// unset the ACME solver keeps the chart's quay.io/jetstack default.
	ACMESolverRepository string
	// EnvoyGatewayNamespace is ingress.gatewayApi.controllerNamespace.
	EnvoyGatewayNamespace string
	// Gateways are the NVCF Gateways the stack names under
	// ingress.gatewayApi.gateways, as sorted, distinct namespace/name entries.
	Gateways []string
	// ExternalComponents are the stack dependencies the stack does not
	// install, because their release condition is off: nats, openbao or
	// cassandra. The validator reads them as NVCF_EXTERNAL_COMPONENTS.
	ExternalComponents []string
	// StorageClass is global.storageClass, the class every stack PVC names.
	// The validator reads it as NVCF_STORAGE_CLASS and then checks that class
	// instead of requiring a default one.
	StorageClass string
}

// LoadStackValues reads files in order and layers each over the previous,
// the way helmfile layers an environment file over base.yaml. Unreadable or
// malformed files are skipped.
func LoadStackValues(files []string) StackValues {
	merged, found := loadValuesFiles(files)
	var chartRegistry, chartRepository string
	if digString(merged, "global", "helm", "sources", "url") == "" {
		chartRegistry = digString(merged, "global", "helm", "sources", "registry")
		chartRepository = strings.Trim(digString(merged, "global", "helm", "sources", "repository"), "/")
	}
	return StackValues{
		Found:                 found,
		ImageRegistry:         digString(merged, "global", "image", "registry"),
		ImageRepository:       strings.Trim(digString(merged, "global", "image", "repository"), "/"),
		ChartRegistry:         chartRegistry,
		ChartRepository:       chartRepository,
		CertManagerEnabled:    releaseCondition(merged, "certManager", "enabled"),
		ACMESolverRepository:  digString(merged, "certManager", "acmesolver", "image", "repository"),
		EnvoyGatewayNamespace: digString(merged, "ingress", "gatewayApi", "controllerNamespace"),
		Gateways:              stackGateways(merged),
		ExternalComponents:    stackExternalComponents(merged),
		StorageClass:          digString(merged, "global", "storageClass"),
	}
}

// stackExternalComponents returns the validator's external components the
// stack leaves out: each release whose condition (nats.enabled,
// openbao.enabled, cassandra.enabled) is set and not true. A component the
// values never mention is not listed; helmfile refuses to evaluate a missing
// condition, so such values do not describe a control-plane install. Sorted.
func stackExternalComponents(merged map[string]any) []string {
	var out []string
	for _, component := range validatorExternalComponents {
		if _, set := merged[component]; set && !releaseCondition(merged, component, "enabled") {
			out = append(out, component)
		}
	}
	return out
}

// validatorExternalComponents are the stack dependencies the validator can be
// told run outside the stack, through NVCF_EXTERNAL_COMPONENTS. Sorted.
var validatorExternalComponents = []string{"cassandra", "nats", "openbao"}

// ParseExternalComponents normalises comma-separated NVCF_EXTERNAL_COMPONENTS
// entries into a sorted, distinct list, and rejects a name the validator does
// not know, which it would otherwise ignore silently.
func ParseExternalComponents(entries []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range entries {
		for _, c := range strings.Split(raw, ",") {
			c = strings.ToLower(strings.TrimSpace(c))
			if c == "" || seen[c] {
				continue
			}
			if !slices.Contains(validatorExternalComponents, c) {
				return nil, fmt.Errorf("unknown component %q: use a subset of %s",
					c, strings.Join(validatorExternalComponents, ", "))
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// stackGateways returns the Gateways the stack wires NVCF routes to, as
// sorted, distinct namespace/name entries, applying the gates the stack
// applies. The ingress release that renders nvcfGatewayRoutes has the
// condition ingress.gatewayApi.enabled. Inside it global.yaml.gotmpl wires
// shared and grpc always, nats only when routes.nats.enabled, and llmGrpc
// and llmQuic only when routes.llmWorker.enabled, each read by a template if.
// An environment file can name a Gateway whose route stays off, and the stack
// never creates or wires that Gateway, so the validator must not look for it.
func stackGateways(merged map[string]any) []string {
	if !releaseCondition(merged, "ingress", "gatewayApi", "enabled") {
		return nil
	}
	wired := []string{"shared", "grpc"}
	if templateTruth(digAny(merged, "ingress", "gatewayApi", "routes", "nats", "enabled")) {
		wired = append(wired, "nats")
	}
	if templateTruth(digAny(merged, "ingress", "gatewayApi", "routes", "llmWorker", "enabled")) {
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

// loadValuesFiles layers files in order and reports whether any was read.
// Unreadable or malformed files are skipped.
func loadValuesFiles(files []string) (map[string]any, bool) {
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
	return merged, found
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

// releaseCondition reads a helmfile release condition, which is on only for
// a YAML boolean true. A quoted "true" leaves the release out.
func releaseCondition(m map[string]any, keys ...string) bool {
	b, _ := digAny(m, keys...).(bool)
	return b
}

// templateTruth reads a value the way a Go template's if does: false, zero,
// empty and nil are false, and anything else is true, a quoted "false"
// included. The stack's route gates are such ifs over a dig.
func templateTruth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int64:
		return x != 0
	case int:
		return x != 0
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
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

// EnumerateRegistries builds the list of registries to credential-check from
// the validator image ref, the stack values (global.image.registry and
// repository, the OCI chart source, and quay.io for the cert-manager ACME
// solver), and the operator-supplied extras. stack is empty when the values
// that describe the install could not be read; then only the image and the
// extras name registries, and nvcr.io is a non-critical guess.
func EnumerateRegistries(imageRef string, stack StackValues, extras []RegistryEntry) []RegistryEntry {
	var out []RegistryEntry

	// add returns the entry that lists the registry, or nil, so a source only
	// counts as having named a registry once its value passed validation. A
	// registry already listed is listed again only for a scope not yet probed:
	// the validator image's org and the stack's org can differ on one registry.
	// An entry for any scope probes the registry, unless exact asks for that
	// scope alone. An entry another source also names is not chart-only.
	add := func(registry, repoHint string, critical, exact bool) *RegistryEntry {
		registry = strings.TrimSpace(registry)
		if registry == "" {
			return nil
		}
		// Every source goes through the same host validation. The probe builds
		// "https://" + registry + "/v2/", so a value carrying "/" or "@" moves
		// the host: global.image.registry set to "nvcr.io@attacker.example.com"
		// would otherwise aim an outbound request wherever that names.
		if host, _ := parseRegistryHostPort(registry); host == "" {
			return nil
		}
		for i, e := range out {
			if e.Registry == registry && (e.RepoHint == repoHint || (repoHint == "" && !exact)) {
				out[i].ChartsOnly = false
				return &out[i]
			}
		}
		out = append(out, RegistryEntry{Registry: registry, RepoHint: repoHint, Critical: critical})
		return &out[len(out)-1]
	}

	// Source 1: base registry from the configured validator image.
	// Carry the repo path as a scope hint so the token exchange uses the
	// operator's actual org rather than a fake one: NGC returns 403 for
	// orgs the API key cannot access, even if the key itself is valid.
	//
	// Critical follows the same rule as every other source rather than being
	// forced true: an air-gapped install that side-loaded the image never
	// contacts this registry at pull time (ImagePullPolicy is IfNotPresent).
	imageNamedRegistry := false
	if reg, repo, _, ok := parseImageRef(imageRef); ok {
		imageNamedRegistry = add(reg, repo, isNGCRegistry(reg), false) != nil
	}

	// Source 2: global.image.registry and global.image.repository from the
	// stack values, which every stack image is pulled from. The probe is
	// scoped to that repository, so a key without access to the stack's org
	// is caught even when the validator image comes from another org. The
	// stack renders registry + "/" + repository, so "harbor.corp.example/nvcf"
	// is a valid registry: probe its host and fold the path into the scope.
	stackNamedRegistry := false
	if host, scope := stackScope(stack.ImageRegistry, stack.ImageRepository); host != "" {
		// If it's an NGC registry, mark critical; customer mirrors are non-critical.
		stackNamedRegistry = add(host, scope, isNGCRegistry(host), false) != nil
	}

	// Source 3: the stack's OCI chart source, global.helm.sources.registry and
	// repository, folded into a host and scope the same way. helm on this
	// machine pulls the stack's charts from it with the docker login, so a
	// login it rejects, or one with no access to that scope, fails the
	// install. It marks the entry already listed for that scope, and is
	// otherwise listed on its own, critical for an NGC registry like the
	// stack's images.
	if host, scope := stackScope(stack.ChartRegistry, stack.ChartRepository); host != "" {
		listed := len(out)
		if e := add(host, scope, isNGCRegistry(host), true); e != nil {
			e.Charts = true
			e.ChartsOnly = len(out) > listed
		}
	}

	// Source 4: cert-manager's ACME solver. global.yaml.gotmpl moves the
	// controller images to the mirror, but the solver keeps the chart's
	// quay.io/jetstack default unless certManager.acmesolver.image is set, so
	// a mirrored site still pulls it from quay.io at its first HTTP-01 order.
	// Probe quay.io unless the stack leaves cert-manager out or points the
	// solver somewhere else. Without stack values, probe it as a guess.
	if acme := stack.ACMESolverRepository; !stack.Found ||
		(stack.CertManagerEnabled && (acme == "" || strings.HasPrefix(acme, certManagerRegistry+"/"))) {
		add(certManagerRegistry, "", false, false)
	}

	// Source 5: operator-supplied extras (--cluster-validator-registries),
	// parsed by ParseRegistryExtra.
	for _, e := range extras {
		add(e.Registry, e.RepoHint, false, false)
	}

	// Fallback for "no source named a registry at all": no validator image is
	// configured and no stack values describe the install. Without this the
	// run reports on quay.io alone while the operator's NGC credentials go
	// unchecked. environments/base.yaml sets global.image.registry to nvcr.io,
	// so it is the right guess when there is nothing else to go on.
	//
	// Deliberately not triggered when the image or the stack named a non-NGC
	// registry: that is a mirrored install, which has no reason to reach
	// nvcr.io, and probing it there would add a failing round trip for the
	// sites least able to make it.
	//
	// Non-critical, unlike an NGC registry a source actually named. A guess
	// must not hard-fail the run, and this category has no opt-out flag.
	if !imageNamedRegistry && !stackNamedRegistry {
		add(ngcRegistry, "", false, false)
	}

	return out
}

// stackScope splits a stack's registry and repository, which the stack
// renders as registry + "/" + repository, into the host to probe and the
// scope to probe it for: "harbor.corp.example/nvcf" is a valid registry, so
// its path is folded into the scope.
func stackScope(registry, repository string) (host, scope string) {
	host, path, _ := strings.Cut(registry, "/")
	if host == "" {
		return "", ""
	}
	scope = strings.Trim(strings.Join([]string{strings.Trim(path, "/"), repository}, "/"), "/")
	// One segment is an org with no team, which is no repository, and nvcr.io
	// answers that scope with 400 "malformed token scope": a probe that could
	// then only report it unverifiable, a revoked key included. With no scope
	// the token endpoint still judges the key.
	if !strings.Contains(scope, "/") {
		scope = ""
	}
	return host, scope
}
