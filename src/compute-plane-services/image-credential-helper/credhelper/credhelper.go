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

package credhelper

import (
	"context"
	"net/url"
	"strings"
	"sync"

	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	orascredentials "oras.land/oras-go/v2/registry/remote/credentials"
)

type CredHelper interface {
	// GetRegistryCredentials will return a username and token for the given OCI-compliant registry specified in ref
	// for the long-lived username/password basic credentials, if ref matches any helper.
	// Otherwise username/password are returned as-is.
	GetRegistryCredentials(ctx context.Context, ref string, creds AuthHelperCredentials) (username, password string, err error)
}

// NewCredHelper returns a CredHelper to fetch tokens for certain CSP Helm/image registries.
func NewCredHelper() CredHelper {
	return credHelper{}
}

type credHelper struct{}

type AuthHelperCredentials struct {
	Username    string
	Password    string
	LoadFromEnv bool
}

type CustomAuthHelper interface {
	Matches(serverURL *url.URL) (match bool, isPublic bool)
	Run(ctx context.Context, refURL *url.URL, creds AuthHelperCredentials) (username, password string, err error)
}

var (
	customAuthHelpersMu sync.Mutex
	customAuthHelpers   = map[string]CustomAuthHelper{
		"ecr":        ecrHelper{},
		"volcengine": volcengineHelper{},
	}
)

func RegisterAuthHelper(name string, h CustomAuthHelper) {
	customAuthHelpersMu.Lock()
	customAuthHelpers[name] = h
	customAuthHelpersMu.Unlock()
}

func getCustomAuthHelpers() []CustomAuthHelper {
	customAuthHelpersMu.Lock()
	authHelpers := make([]CustomAuthHelper, 0, len(customAuthHelpers))
	for _, h := range customAuthHelpers {
		authHelpers = append(authHelpers, h)
	}
	customAuthHelpersMu.Unlock()
	return authHelpers
}

func (c credHelper) GetRegistryCredentials(ctx context.Context, ref string, creds AuthHelperCredentials) (string, string, error) {
	return c.getRegistryCredentials(ctx, getCustomAuthHelpers(), ref, creds)
}

func (c credHelper) getRegistryCredentials(
	ctx context.Context,
	customAuthHelpers []CustomAuthHelper,
	ref string,
	creds AuthHelperCredentials,
) (username, password string, err error) {
	refURL, err := parseRef(ref)
	if err != nil {
		return "", "", err
	}

	for _, authHelper := range customAuthHelpers {
		if match, isPublic := authHelper.Matches(refURL); match {
			if isPublic {
				return "", "", nil
			}
			return authHelper.Run(ctx, refURL, creds)
		}
	}

	return creds.Username, creds.Password, nil
}

func parseRef(ref string) (*url.URL, error) {
	ref = strings.TrimPrefix(ref, "//")
	if !strings.Contains(ref, "://") {
		ref = ociRegistryScheme + "://" + ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return nil, err
	}
	return refURL, nil
}

type inMemoryCredsStore struct {
	mu sync.RWMutex
	m  map[string]orasauth.Credential
}

// NewCredentialStore returns an in-memory creds store, safe for concurrent use.
func NewCredentialStore() orascredentials.Store {
	return &inMemoryCredsStore{m: map[string]orasauth.Credential{}}
}

func (s *inMemoryCredsStore) Get(_ context.Context, serverAddress string) (orasauth.Credential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[serverAddress], nil
}

func (s *inMemoryCredsStore) Put(_ context.Context, serverAddress string, cred orasauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[serverAddress] = cred
	return nil
}

func (s *inMemoryCredsStore) Delete(_ context.Context, serverAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, serverAddress)
	return nil
}
