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

// Package statickeys authenticates gateway callers against a mounted file of
// SHA-256 API key digests. It is the in-process InvocationAuthClient used in
// static key mode, where there is no NVCF API to call.
package statickeys

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/nvcf"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// ReloadInterval bounds how often the key file is re-read. It matches the
// NVCF API token cache TTL so both secrets rotate on the same cadence.
const ReloadInterval = 60 * time.Second

// ClientAuthIDPrefix prefixes the key id in InvocationAuthResponse.ClientAuthID.
const ClientAuthIDPrefix = "api-key:"

// Key ids end up in rate-limit store keys and logs, so they are restricted to
// a conservative character set with no separators.
var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var errAuthenticationFailed = status.Error(codes.Unauthenticated, "authentication failed")

// File is the on-disk key file format.
type File struct {
	Keys []FileKey `json:"keys"`
}

// FileKey is one accepted API key: an id used for attribution and rate
// limiting, and the lowercase hex SHA-256 digest of the key itself.
type FileKey struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type key struct {
	id     string
	digest []byte
}

// Authorizer implements the gateway's InvocationAuthClient with static API
// keys. It is safe for concurrent use.
type Authorizer struct {
	path     string
	interval time.Duration
	now      func() time.Time
	compare  func(x, y []byte) int

	mu           sync.Mutex
	keys         []key
	loadErr      error
	expiresAt    time.Time
	loadedDigest [sha256.Size]byte
	loggedErr    string
}

// New returns an Authorizer for the key file at path. The file is read on the
// first request and at most once per ReloadInterval afterwards; until it is
// present and valid every request is rejected.
func New(path string) (*Authorizer, error) {
	if path == "" {
		return nil, errors.New("api keys path is required")
	}
	return &Authorizer{
		path:     path,
		interval: ReloadInterval,
		now:      time.Now,
		compare:  subtle.ConstantTimeCompare,
	}, nil
}

// AuthorizeInvocation verifies clientAuthorizationToken against the key file.
// The routing key is ignored: static key mode has none. Every failure is
// reported as codes.Unauthenticated so the gateway answers 401.
func (a *Authorizer) AuthorizeInvocation(
	ctx context.Context,
	clientAuthorizationToken string,
	_ string,
) (*nvcf.InvocationAuthResponse, error) {
	keys, err := a.currentKeys(ctx)
	if err != nil || clientAuthorizationToken == "" {
		return nil, errAuthenticationFailed
	}

	presented := sha256.Sum256([]byte(clientAuthorizationToken))
	// Compare against every key without returning early so the time taken
	// does not reveal which key, if any, matched. Digests are unique within
	// a file, so at most one key can match.
	matched := -1
	for i := range keys {
		equal := a.compare(presented[:], keys[i].digest)
		matched = subtle.ConstantTimeSelect(equal, i, matched)
	}
	if matched < 0 {
		return nil, errAuthenticationFailed
	}

	id := keys[matched].id
	return &nvcf.InvocationAuthResponse{
		RoutingKey:   "",
		ClientAuthID: ClientAuthIDPrefix + id,
		RateLimitKey: id,
	}, nil
}

// Close satisfies nvcf.Client; the authorizer holds no connections.
func (a *Authorizer) Close() error {
	return nil
}

func (a *Authorizer) currentKeys(ctx context.Context) ([]key, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	if now.Before(a.expiresAt) {
		return a.keys, a.loadErr
	}
	a.expiresAt = now.Add(a.interval)

	data, err := os.ReadFile(a.path)
	var keys []key
	if err == nil {
		keys, err = parse(data)
	}
	if err != nil {
		// Fail closed: a missing or malformed file revokes every key rather
		// than keeping the last good set, so deleting the file is a way to
		// shut callers out.
		a.keys, a.loadErr = nil, err
		a.loadedDigest = [sha256.Size]byte{}
		if msg := err.Error(); msg != a.loggedErr {
			a.loggedErr = msg
			telemetry.Logger(ctx).Error().
				Err(err).
				Str("api_keys_path", a.path).
				Msg("static API key file unusable; rejecting all requests until it is valid")
		}
		return nil, err
	}

	a.keys, a.loadErr = keys, nil
	a.loggedErr = ""
	if digest := sha256.Sum256(data); digest != a.loadedDigest {
		a.loadedDigest = digest
		telemetry.Logger(ctx).Info().
			Str("api_keys_path", a.path).
			Int("key_count", len(keys)).
			Msg("loaded static API key file")
	}
	return keys, nil
}

func parse(data []byte) ([]key, error) {
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode api key file: %w", err)
	}
	if len(file.Keys) == 0 {
		return nil, errors.New("api key file contains no keys")
	}

	keys := make([]key, 0, len(file.Keys))
	seenIDs := make(map[string]struct{}, len(file.Keys))
	seenDigests := make(map[string]struct{}, len(file.Keys))
	for i, entry := range file.Keys {
		if !keyIDPattern.MatchString(entry.ID) {
			return nil, fmt.Errorf(
				"api key file entry %d: id must match %s", i, keyIDPattern.String(),
			)
		}
		if _, dup := seenIDs[entry.ID]; dup {
			return nil, fmt.Errorf("api key file entry %d: duplicate id %q", i, entry.ID)
		}
		digest, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf(
				"api key file entry %d (id %q): sha256 must be %d hex characters",
				i, entry.ID, 2*sha256.Size,
			)
		}
		if _, dup := seenDigests[string(digest)]; dup {
			return nil, fmt.Errorf("api key file entry %d (id %q): duplicate sha256", i, entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
		seenDigests[string(digest)] = struct{}{}
		keys = append(keys, key{id: entry.ID, digest: digest})
	}
	return keys, nil
}
