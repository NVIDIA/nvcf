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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ngcKeyRegistryHost is the one registry NGC_API_KEY is for: the registry up
// mints its pull secrets for from that key. isNGCRegistry also accepts other
// NVIDIA hosts (staging NGC, internal registries), and those take their own
// logins, never the NGC key.
const ngcKeyRegistryHost = "nvcr.io"

// isNGCKeyRegistry reports whether NGC_API_KEY may be sent to registry.
func isNGCKeyRegistry(registry string) bool {
	if !isBareRegistryHost(registry) {
		return false
	}
	host := registry
	if h, _, err := net.SplitHostPort(registry); err == nil {
		host = h
	}
	return strings.EqualFold(host, ngcKeyRegistryHost)
}

// registryCredential is a local credential for one registry.
type registryCredential struct {
	user, pass string
	// source says where it was read: an environment variable, the docker
	// config file, or a docker-credential-<name> helper.
	source string
	// ngcKey marks the NGC API key read from the environment.
	ngcKey bool
}

// RegistryCredentials resolves the local credential for a registry the way
// docker does, once per registry per run, and settles which credential the run
// uses for each repository scope on it. The credential probe, tag discovery
// and the validator's pull secret all read the settled one, so the credential
// the local row checks is the one the validator Job is given.
type RegistryCredentials struct {
	// preferNGCKey puts the NGC API key ahead of the docker config for
	// nvcr.io. That is right only where up mints its pull secrets from the
	// key; anywhere else the docker login is what docker and the cluster use,
	// and the key is sent for a scope only when there is no login, or the
	// registry rejects the login or gives it no access to that scope. Either
	// way the row for the stack's chart scope judges the docker login, which
	// helm pulls the charts with: see judgeLogin.
	preferNGCKey bool

	mu     sync.Mutex
	cache  map[string]credentialLookup
	scopes map[credentialScope]*scopeSettlement
}

type credentialLookup struct {
	cred registryCredential
	ok   bool
	err  error
	// fallback is the credential that may take cred's place for a scope: the
	// NGC key behind a docker login for nvcr.io.
	fallback *registryCredential
}

// credentialScope is one repository scope on a registry. nvcr.io entitles a
// credential per org and team, so a docker login and the NGC key can each
// reach a scope the other cannot.
type credentialScope struct{ registry, repo string }

// scopeSettlement holds one scope's settled credential. lock admits one
// caller at a time, so the callers that arrive while it settles wait for its
// result instead of each probing the registry.
type scopeSettlement struct {
	lock    chan struct{}
	settled bool
	result  settledCredential
}

// settledCredential is the credential the run uses for one repository scope.
type settledCredential struct {
	cred registryCredential
	ok   bool
	err  error
	// rejectedLogin is the docker login the registry answered 401 for this
	// scope, before the NGC key took its place.
	rejectedLogin *registryCredential
	// noAccessLogin is a docker login the registry gave no access to this
	// scope, a 403, before the NGC key took its place. Its access, not the
	// login itself, is what the registry refused.
	noAccessLogin *registryCredential
	// fallbackFailure says why the NGC key, tried for a scope the docker
	// login had no access to, did not take its place.
	fallbackFailure string
}

// NewRegistryCredentials returns a per-run credential resolver.
func NewRegistryCredentials(preferNGCKey bool) *RegistryCredentials {
	return &RegistryCredentials{preferNGCKey: preferNGCKey, cache: map[string]credentialLookup{},
		scopes: map[credentialScope]*scopeSettlement{}}
}

type registryCredentialsKey struct{}

// WithRegistryCredentials returns ctx carrying rc, which every registry
// credential lookup made under ctx uses.
func WithRegistryCredentials(ctx context.Context, rc *RegistryCredentials) context.Context {
	return context.WithValue(ctx, registryCredentialsKey{}, rc)
}

// registryCredentialsFrom returns the resolver ctx carries, or a fresh one
// that prefers the NGC key, the order up mints from.
func registryCredentialsFrom(ctx context.Context) *RegistryCredentials {
	if rc, ok := ctx.Value(registryCredentialsKey{}).(*RegistryCredentials); ok && rc != nil {
		return rc
	}
	return NewRegistryCredentials(true)
}

// read returns registry's local credentials as docker and the environment
// hold them, before any scope is settled. ok is false when there is none; err
// reports a credential store that could not be read, which is not the same as
// no credential. A read the caller's ctx cut short is not cached.
func (rc *RegistryCredentials) read(ctx context.Context, registry string) credentialLookup {
	key := strings.ToLower(registry)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if hit, ok := rc.cache[key]; ok {
		return hit
	}
	found := rc.resolve(ctx, registry)
	if ctx.Err() == nil {
		rc.cache[key] = found
	}
	return found
}

// lookup returns the credential the run uses for repo on registry.
//
// Where nvcr.io has a docker login with the NGC key behind it, the scope is
// settled first: a 401 to the login moves the scope to the key, and a 403,
// no access to the scope, tries the key for it. Each scope is settled once,
// and callers for it wait for that result, so the row, tag discovery and the
// validator's pull secret get one credential. Another scope is settled on its
// own. A result the caller's ctx cut short, or that a fault of the registry
// left open, is not kept.
func (rc *RegistryCredentials) lookup(ctx context.Context, registry, repo string) settledCredential {
	return rc.settle(ctx, registry, repo, nil)
}

// lookupChallenged is lookup for a caller that already holds the registry's
// WWW-Authenticate challenge, so settling does not ask /v2/ for it again.
func (rc *RegistryCredentials) lookupChallenged(
	ctx context.Context, registry, repo, challenge string,
) settledCredential {
	return rc.settle(ctx, registry, repo, &challenge)
}

func (rc *RegistryCredentials) settle(ctx context.Context, registry, repo string, challenge *string) settledCredential {
	read := rc.read(ctx, registry)
	if !read.ok || read.fallback == nil {
		return settledCredential{cred: read.cred, ok: read.ok, err: read.err}
	}
	s := rc.settlement(registry, repo)
	select {
	case s.lock <- struct{}{}:
	case <-ctx.Done():
		return settledCredential{err: ctx.Err()}
	}
	defer func() { <-s.lock }()
	if s.settled {
		return s.result
	}
	result, decided := settleScope(ctx, registry, repo, challenge, read.cred, *read.fallback)
	if decided && ctx.Err() == nil {
		s.settled, s.result = true, result
	}
	return result
}

func (rc *RegistryCredentials) settlement(registry, repo string) *scopeSettlement {
	key := credentialScope{registry: strings.ToLower(registry), repo: repo}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	s, ok := rc.scopes[key]
	if !ok {
		s = &scopeSettlement{lock: make(chan struct{}, 1)}
		rc.scopes[key] = s
	}
	return s
}

// credentialSettleTimeout bounds the registry round trips that settle one
// scope. A var so tests can shorten it.
var credentialSettleTimeout = registryProbeTimeout

// settleScope decides between a docker login and the NGC key behind it for
// repo, asking /v2/ for the challenge when the caller has none. Only a 401
// says the login is rejected. A 403 says it has no access to repo, and the
// key is tried for repo alone. decided is false for an answer that says
// nothing about either credential.
func settleScope(
	ctx context.Context, registry, repo string, challenge *string, login, key registryCredential,
) (settledCredential, bool) {
	sctx, cancel := context.WithTimeout(ctx, credentialSettleTimeout)
	defer cancel()
	client := newRegistryHTTPClient(credentialSettleTimeout)
	keep := settledCredential{cred: login, ok: true}
	if challenge == nil {
		resp, err := doRegistryRequest(sctx, client, func() (*http.Request, error) {
			return http.NewRequestWithContext(sctx, http.MethodGet, "https://"+registry+"/v2/", nil)
		})
		if err != nil {
			return keep, false
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			// Anyone may read /v2/, which tells nothing about either credential.
			return keep, true
		case http.StatusUnauthorized:
		default:
			return keep, false
		}
		got := selectAuthChallenge(resp.Header.Values("Www-Authenticate"))
		challenge = &got
	}
	if scheme := authChallengeScheme(*challenge); scheme != "" && !strings.EqualFold(scheme, "Bearer") {
		return keep, true
	}
	_, err := exchangeBearerToken(sctx, client, registry, repo, *challenge, &login)
	var te *tokenExchangeError
	switch {
	case err == nil:
		return keep, true
	case !errors.As(err, &te):
		return keep, false
	case te.refused:
		return keep, true
	case te.credentialed && te.status == http.StatusUnauthorized:
		return settledCredential{cred: key, ok: true, rejectedLogin: &login}, true
	case !te.credentialed || te.status != http.StatusForbidden:
		return keep, false
	}
	_, err = exchangeBearerToken(sctx, client, registry, repo, *challenge, &key)
	switch {
	case err == nil:
		return settledCredential{cred: key, ok: true, noAccessLogin: &login}, true
	case errors.As(err, &te) && (te.rejected() || te.refused):
		keep.fallbackFailure = key.source + " was tried for it too: " + te.msg
		return keep, true
	}
	return keep, false
}

// judgeLogin returns settled with the docker login judged for repo where the
// NGC key went first, so settling never sent the login: a 401 marks it
// rejected and a 403 marks it without access to repo, as settleScope does.
// The credential the run uses for repo stays the key. A scope helm on this
// machine pulls the stack's charts from needs this: helm sends the docker
// login whatever order the run sends credentials in. An answer that says
// nothing about the login, or no docker login besides the key, leaves
// settled as it was.
func (rc *RegistryCredentials) judgeLogin(
	ctx context.Context, registry, repo, challenge string, settled settledCredential,
) settledCredential {
	if !rc.preferNGCKey || !settled.ok || !settled.cred.ngcKey {
		return settled
	}
	login, ok, _ := credsFromDockerConfig(ctx, registry)
	if !ok || login.pass == settled.cred.pass {
		return settled
	}
	jctx, cancel := context.WithTimeout(ctx, credentialSettleTimeout)
	defer cancel()
	_, err := exchangeBearerToken(jctx, newRegistryHTTPClient(credentialSettleTimeout), registry, repo, challenge,
		&login)
	var te *tokenExchangeError
	switch {
	case !errors.As(err, &te) || !te.credentialed:
	case te.status == http.StatusUnauthorized:
		settled.rejectedLogin = &login
	case te.status == http.StatusForbidden:
		settled.noAccessLogin = &login
	}
	return settled
}

func (rc *RegistryCredentials) resolve(ctx context.Context, registry string) credentialLookup {
	var fromKey *registryCredential
	if isNGCKeyRegistry(registry) {
		if name, key := firstSetEnv(ngcAPIKeyEnvNames...); key != "" {
			fromKey = &registryCredential{user: "$oauthtoken", pass: key, source: name, ngcKey: true}
		}
	}
	if fromKey != nil && rc.preferNGCKey {
		return credentialLookup{cred: *fromKey, ok: true}
	}
	cred, ok, err := credsFromDockerConfig(ctx, registry)
	switch {
	case ok:
		if fromKey != nil && fromKey.pass == cred.pass {
			fromKey = nil
		}
		return credentialLookup{cred: cred, ok: true, fallback: fromKey}
	case fromKey != nil:
		return credentialLookup{cred: *fromKey, ok: true}
	}
	return credentialLookup{err: err}
}

// firstSetEnv returns the first of names set to a non-empty value, and that
// value.
func firstSetEnv(names ...string) (string, string) {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return name, v
		}
	}
	return "", ""
}

// dockerConfigPath is where docker reads its config: $DOCKER_CONFIG, else
// ~/.docker.
func dockerConfigPath() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "config.json"), nil
}

// dockerConfigAuth is one auths entry of a docker config.
type dockerConfigAuth struct {
	Auth     string `json:"auth"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func credsFromDockerConfig(ctx context.Context, registry string) (registryCredential, bool, error) {
	path, err := dockerConfigPath()
	if err != nil {
		return registryCredential{}, false, nil
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return registryCredential{}, false, nil
	}
	if err != nil {
		return registryCredential{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Auths       map[string]dockerConfigAuth `json:"auths"`
		CredsStore  string                      `json:"credsStore"`
		CredHelpers map[string]string           `json:"credHelpers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return registryCredential{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	// Docker's order: a per-registry credHelpers entry, else credsStore, and
	// only with neither configured the inline auths entry. With a store
	// configured docker never reads an inline password, so an old one left in
	// the file must not win here either: that sent a stale key while `docker
	// pull` worked.
	key := dockerConfigKey(registry)
	helper, ok := doc.CredHelpers[key]
	if !ok {
		for k, h := range doc.CredHelpers {
			if sameDockerHost(k, key) {
				helper = h
				break
			}
		}
	}
	if helper == "" {
		helper = doc.CredsStore
	}
	if helper != "" {
		return credsFromHelper(ctx, helper, key)
	}
	entry, ok := doc.Auths[key]
	if !ok {
		// Docker matches a legacy key, with a scheme or a /v1/ path, by its
		// hostname.
		for k, e := range doc.Auths {
			if sameDockerHost(k, key) {
				entry, ok = e, true
				break
			}
		}
	}
	if !ok {
		return registryCredential{}, false, nil
	}
	user, pass, ok := decodeDockerConfigAuth(entry)
	if !ok {
		return registryCredential{}, false, nil
	}
	return registryCredential{user: user, pass: pass, source: "docker config " + path}, true, nil
}

func decodeDockerConfigAuth(entry dockerConfigAuth) (string, string, bool) {
	if entry.Username != "" && entry.Password != "" {
		return entry.Username, entry.Password, true
	}
	if entry.Auth == "" {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok
}

// dockerHubConfigKey is the key docker stores Docker Hub logins under.
const dockerHubConfigKey = "https://index.docker.io/v1/"

// dockerHubRegistry is the host Docker Hub images are pulled from.
const dockerHubRegistry = "registry-1.docker.io"

// dockerConfigKey maps a registry host to the key docker uses for it in
// config.json. Docker Hub's hosts all share one legacy key; every other
// registry is keyed by its host.
func dockerConfigKey(registry string) string {
	if isDockerHubHost(registry) {
		return dockerHubConfigKey
	}
	return registry
}

func isDockerHubHost(host string) bool {
	switch strings.ToLower(host) {
	case "docker.io", "index.docker.io", dockerHubRegistry:
		return true
	}
	return false
}

// dockerHostname reduces a docker config key to its host the way docker's
// file store does: scheme and anything from the first '/' dropped, with
// Docker Hub's hosts folded into one.
func dockerHostname(key string) string {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://")
	host, _, _ := strings.Cut(key, "/")
	host = strings.ToLower(host)
	if isDockerHubHost(host) {
		return "docker.io"
	}
	return host
}

func sameDockerHost(a, b string) bool {
	return dockerHostname(a) == dockerHostname(b)
}

// credentialHelperName matches the docker-credential-<name> suffixes docker
// accepts. Anything else is refused rather than put on an exec path.
var credentialHelperName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// credentialHelperTimeout bounds one helper call; a helper waiting on a
// keychain prompt must not stall preflight. A var so tests can allow for a
// loaded machine starting a freshly written helper script.
var credentialHelperTimeout = 5 * time.Second

// credsFromHelper asks docker-credential-<helper> for registry's credential
// using the credential-helper protocol docker uses: "get" with the server URL
// on stdin, JSON {"Username","Secret"} on stdout. The call ends with ctx, so
// it stays inside the caller's time bound and stops on an interrupt.
func credsFromHelper(ctx context.Context, helper, serverURL string) (registryCredential, bool, error) {
	if !credentialHelperName.MatchString(helper) {
		return registryCredential{}, false, fmt.Errorf("refusing credential helper name %q", helper)
	}
	hctx, cancel := context.WithTimeout(ctx, credentialHelperTimeout)
	defer cancel()
	name := "docker-credential-" + helper
	cmd := exec.CommandContext(hctx, name, "get")
	cmd.Stdin = strings.NewReader(serverURL)
	// Without WaitDelay a helper that leaves a child holding stdout open keeps
	// Output waiting past the timeout. The output cap keeps a misbehaving
	// helper from filling memory; a credential is a few hundred bytes.
	cmd.WaitDelay = time.Second
	var out cappedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// The protocol's answer for a registry the store has nothing for.
		if strings.Contains(out.buf.String(), "credentials not found") {
			return registryCredential{}, false, nil
		}
		if hctx.Err() != nil {
			return registryCredential{}, false, fmt.Errorf("%s did not answer within %s", name, credentialHelperTimeout)
		}
		return registryCredential{}, false, fmt.Errorf("%s: %w", name, err)
	}
	if out.overflow {
		return registryCredential{}, false, fmt.Errorf("%s wrote more than %d bytes", name, credentialHelperOutputLimit)
	}
	var cred struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(out.buf.Bytes(), &cred); err != nil {
		return registryCredential{}, false, fmt.Errorf("%s: decode its answer: %w", name, err)
	}
	if cred.Secret == "" {
		return registryCredential{}, false, nil
	}
	return registryCredential{user: cred.Username, pass: cred.Secret, source: name}, true, nil
}

// credentialHelperOutputLimit caps what a credential helper may write.
const credentialHelperOutputLimit = 64 << 10

// cappedBuffer keeps at most credentialHelperOutputLimit bytes and records
// whether more were written.
type cappedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := credentialHelperOutputLimit - c.buf.Len(); len(p) > room {
		c.overflow = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}
