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

package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
)

//nolint:gosec
const (
	ClientIDEnv     = "OAUTH_CLIENT_ID"
	ClientSecretEnv = "OAUTH_CLIENT_SECRET_KEY"
)

type TokenFetcherResultListener interface {
	// OnFetchTokenResponse returns the status code for each FetchToken
	OnFetchTokenResponse(respStatusCode int)
}

// TokenFetcher implements core.tokenFetcher, which could be used
// in core.JWTCache
type TokenFetcher struct {
	tokenURL   string
	tokenScope string
	clientID   string

	getAuthClientSecret func(*TokenFetcher) (string, error)

	client *http.Client

	resultListeners         []TokenFetcherResultListener
	scopeEnforcementEnabled bool
	envNameFromFile         string
}

type authTokenResponse struct {
	// Token is the issued JWT
	Token             string `json:"access_token"` //nolint:gosec // G117: false positive - this is a response struct, not a secret storage
	Type              string `json:"token_type"`
	ExpirationSeconds uint64 `json:"expires_in,omitempty"`
	Scope             string `json:"scope"`
}

const clientTimeout = 5 * time.Second

type TokenFetcherOption func(*TokenFetcher)

func WithResultListener(listener TokenFetcherResultListener) TokenFetcherOption {
	return func(f *TokenFetcher) {
		f.resultListeners = append(f.resultListeners, listener)
	}
}

// WithScopeEnforcementEnabled ensures that the returned scope from the Auth server contains
// at least all requested scopes
func WithScopeEnforcementEnabled(scopeEnforcementEnabled bool) TokenFetcherOption {
	return func(sf *TokenFetcher) {
		sf.scopeEnforcementEnabled = scopeEnforcementEnabled
	}
}

func WithEnvKey(env string) TokenFetcherOption {
	return func(f *TokenFetcher) {
		f.envNameFromFile = env
	}
}

func NewTokenFetcher(tokenURL, clientID, secretKey, tokenScope string, opts ...TokenFetcherOption) *TokenFetcher {
	getAuthClientSecretFunc := func(*TokenFetcher) (string, error) {
		return secretKey, nil
	}
	return newTokenFetcher(tokenURL, tokenScope, clientID, getAuthClientSecretFunc, opts...)
}

func NewTokenFetcherFromFile(ctx context.Context,
	tokenURL, tokenScope, clientID, envFile string,
	opts ...TokenFetcherOption) (*TokenFetcher, error) {
	keyFileFetcher, err := cmnsecret.NewKeyFileFetcher(ctx, cmnsecret.WithSecretKeyFile(envFile))
	if err != nil {
		return nil, err
	}
	getAuthClientSecretFunc := func(f *TokenFetcher) (string, error) {
		data, err := keyFileFetcher.FetchSecretKey(ctx)
		if err != nil {
			return "", err
		}
		return getClientSecretFromEnvFile(data, f.envNameFromFile), nil
	}
	f := newTokenFetcher(tokenURL, tokenScope, clientID, getAuthClientSecretFunc, opts...)
	return f, nil
}

func newTokenFetcher(tokenURL, tokenScope, clientID string,
	getAuthClientSecretFunc func(*TokenFetcher) (string, error),
	opts ...TokenFetcherOption) *TokenFetcher {
	f := &TokenFetcher{
		tokenURL:            tokenURL,
		tokenScope:          tokenScope,
		clientID:            clientID,
		getAuthClientSecret: getAuthClientSecretFunc,
		client:              &http.Client{Timeout: clientTimeout},
	}
	// Add OTEL instrumentation for requests to Auth server
	f.client.Transport = otelhttp.NewTransport(f.client.Transport)
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// nolint
func (f *TokenFetcher) RefreshClient() {}

func (f *TokenFetcher) FetchToken(ctx context.Context) (string, error) {
	log := core.GetLogger(ctx).WithFields(map[string]interface{}{
		"rpc":      "auth.FetchToken",
		"endpoint": f.tokenURL,
	})
	log.Debug("Fetching Auth token")

	// prepare the form data
	requestScope := strings.TrimSpace(f.tokenScope)
	fd := url.Values{}
	fd.Set("grant_type", "client_credentials")
	fd.Set("scope", requestScope)

	// prepare the request with encoded data
	req, err := http.NewRequestWithContext(ctx, "POST", f.tokenURL, strings.NewReader(fd.Encode()))
	if err != nil {
		log.WithError(err).Errorf("http.NewRequestWithContext() failed")
		return "", err
	}

	clientSecret, err := f.getAuthClientSecret(f)
	if err != nil {
		return "", err
	}

	// set auth
	req.SetBasicAuth(strings.TrimSpace(f.clientID), strings.TrimSpace(clientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.client.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
	if err != nil {
		log.WithError(err).Errorf("client.Do() failed")
		f.invokeResultListener(0)
		return "", err
	}
	defer resp.Body.Close()
	log.Debugf("Received Auth token response with status %d", resp.StatusCode)
	f.invokeResultListener(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		// Limit body read to 1K
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if err != nil {
			log.WithError(err).Error("failed to read response body")
			body = []byte{}
		}
		log.WithField("body", string(body)).Errorf("resp.StatusCode is %d, not http.StatusOK", resp.StatusCode)
		return "", core.HTTPCodeError(resp.StatusCode)
	}
	log.Debug("Auth token response was http.StatusOK")

	tokenResp := &authTokenResponse{}
	if err := json.NewDecoder(resp.Body).Decode(tokenResp); err != nil {
		log.WithError(err).Error("failed to deserialize resp.Body as TokenResponse")
		return "", fmt.Errorf("failed to deserialize resp.Body as TokenResponse, err: %w", err)
	}

	jwt := tokenResp.Token
	if jwt == "" {
		log.Error("authTokenResponse.Token is empty")
		return "", fmt.Errorf("authTokenResponse.Token is empty")
	}

	// Verify scopes from response match scopes from request
	if f.scopeEnforcementEnabled {
		if err := verifyStrictScopeEnforcement(f.clientID, requestScope, tokenResp.Scope); err != nil {
			log.WithError(err).Error("strict scope verification failed")
			return "", err
		}
	}

	return jwt, nil
}

func verifyStrictScopeEnforcement(clientID, requestScopes, responseScopes string) error {
	requestScopesList := strings.Split(strings.TrimSpace(requestScopes), " ")
	responseScopesList := strings.Split(strings.TrimSpace(responseScopes), " ")
	var missingScopes []string
	for _, reqScope := range requestScopesList {
		if !slices.Contains(responseScopesList, reqScope) {
			missingScopes = append(missingScopes, reqScope)
		}
	}
	if len(missingScopes) > 0 {
		return fmt.Errorf("the requested scopes '%s' did not match granted scopes '%s'. "+
			"Please let the Auth service owner know that your client '%s' requires the following additional scopes: %s",
			requestScopes, responseScopes, clientID, strings.Join(missingScopes, ", "))
	}
	return nil
}

func (f *TokenFetcher) invokeResultListener(httpStatusCode int) {
	for _, l := range f.resultListeners {
		l.OnFetchTokenResponse(httpStatusCode)
	}
}

func getClientSecretFromEnvFile(data, altEnv string) string {
	scanner := bufio.NewScanner(strings.NewReader(data))
	var env string
	if altEnv != "" {
		env = altEnv
	} else {
		env = ClientSecretEnv
	}
	prefix := fmt.Sprintf("%s=", env)
	for scanner.Scan() {
		txt := scanner.Text()
		if strings.HasPrefix(txt, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(txt, prefix))
		}
	}
	// If no prefix found, treat the entire content as the secret
	return strings.TrimSpace(data)
}
