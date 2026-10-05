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
// docker does, once per registry per run. The credential probe, tag discovery
// and the validator's pull secret all read it, so the credential the local
// row checks is the one the validator Job is given.
type RegistryCredentials struct {
	// preferNGCKey puts the NGC API key ahead of the docker config for
	// nvcr.io. That is right only where up mints its pull secrets from the
	// key; anywhere else the docker login is what docker and the cluster use,
	// and the key is sent only when there is no login or the registry
	// rejects it.
	preferNGCKey bool

	mu    sync.Mutex
	cache map[string]credentialLookup
}

type credentialLookup struct {
	cred registryCredential
	ok   bool
	err  error
	// fallback is the credential to send once the registry rejects cred:
	// the NGC key behind a docker login for nvcr.io.
	fallback *registryCredential
	// rejectedLogin is the source of the docker login the registry rejected
	// before cred, the fallback, took its place.
	rejectedLogin string
}

// NewRegistryCredentials returns a per-run credential resolver.
func NewRegistryCredentials(preferNGCKey bool) *RegistryCredentials {
	return &RegistryCredentials{preferNGCKey: preferNGCKey, cache: map[string]credentialLookup{}}
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

// lookup returns registry's local credential. ok is false when there is none;
// err reports a credential store that could not be read, which is not the
// same as no credential. A lookup the caller's ctx cut short is not cached.
func (rc *RegistryCredentials) lookup(ctx context.Context, registry string) (registryCredential, bool, error) {
	key := strings.ToLower(registry)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if hit, ok := rc.cache[key]; ok {
		return hit.cred, hit.ok, hit.err
	}
	found := rc.resolve(ctx, registry)
	if ctx.Err() == nil {
		rc.cache[key] = found
	}
	return found.cred, found.ok, found.err
}

// rejected records that registry refused cred and returns the credential to
// send instead, if there is one: the NGC key behind a docker login for
// nvcr.io. Later lookups return it, so tag discovery, the credential row and
// the validator's pull secret all move to the credential the registry has not
// refused.
func (rc *RegistryCredentials) rejected(registry string, cred registryCredential) (registryCredential, bool) {
	key := strings.ToLower(registry)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	hit, ok := rc.cache[key]
	switch {
	case !ok || !hit.ok:
		return registryCredential{}, false
	case hit.cred != cred:
		// Another check already moved past cred.
		return hit.cred, true
	case hit.fallback == nil:
		return registryCredential{}, false
	}
	next := *hit.fallback
	rc.cache[key] = credentialLookup{cred: next, ok: true, rejectedLogin: cred.source}
	return next, true
}

// rejectedLogin returns the source of the docker login registry rejected
// before the run moved to the NGC key, or "".
func (rc *RegistryCredentials) rejectedLogin(registry string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.cache[strings.ToLower(registry)].rejectedLogin
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
