/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"context"
	"errors"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnoauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/oauth"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
)

const (
	DefaultJWKSKeySetTTL = 60 * time.Minute
	// TokenExpiryMargin is the margin to add to the token expiry time to
	// we use this time since OAuth JWTs in production are good for 15 minutes
	// and we want to ensure we refresh the token well before it expires
	TokenExpiryMargin = 3 * time.Minute
)

// TokenFetcher represents a struct that can fetch tokens for clients
type TokenFetcher interface {
	// FetchToken returns the token in the cache if it think the token is
	// valid, otherwise, it tries to fetch a new token and update cache
	// first.
	FetchToken(ctx context.Context) (string, error)
}

// TokenFetcherOptions
type TokenFetcherOptions struct {
	SelfHostedEnabled               bool
	OAuthClientID                   string
	OAuthClientSecretKey            string
	OAuthClientSecretsEnvFile       string
	OAuthTokenFetchFailureThreshold uint64
	TokenURL                        string
	OAuthTokenScope                 string
	OAuthPublicKeysetEndpoint       string
	NGCServiceAPIKeyFile            string
	NGCServiceAPIKey                string
	SelfHostedVaultSecretsJSONPath  string
	// PSATTokenFilePath, when set, takes precedence over every other source:
	// the fetcher simply reads the projected SA token from this path. Used by
	// self-hosted clusters running the PSAT identity-source flow.
	PSATTokenFilePath string
}

// NewTokenFetcher returns the tokenfetcher and a health check to
// add to the liveness probe if appropriate
func NewTokenFetcher(ctx context.Context, name string, opts TokenFetcherOptions) (TokenFetcher, *health.TokenFetcherHealthCheck, error) {
	// PSAT mode: read projected SA token from file. Takes precedence over the
	// legacy vault-based self-hosted path so callers can opt into OIDC/PSAT
	// without unsetting SelfHostedEnabled.
	if opts.PSATTokenFilePath != "" {
		fetcher, err := NewPSATTokenFetcher(opts.PSATTokenFilePath)
		if err != nil {
			return nil, nil, err
		}
		return fetcher, health.SuccessfulTokenFetcherHealthCheck(name + "-psat-tokenfetcher"), nil
	}
	// If self-hosted mode is enabled, use the self-hosted vault secrets JSON path
	if opts.SelfHostedEnabled {
		return NewSelfManagedSecretsFetcher(ctx, name, opts.SelfHostedVaultSecretsJSONPath)
	} else if opts.OAuthClientID != "" {
		// instantiate OAuthTokenFetcher to setup the JWTCache below
		if opts.OAuthClientSecretKey == "" && opts.OAuthClientSecretsEnvFile == "" {
			return nil, nil, errors.New("either OAuth client ID and secret, or secret env file, is required")
		}
		name += "-oauth-tokenfetcher"
		tokenFetcherHealthCheck := health.NewTokenFetcherHealthCheck(name, health.WithUnauthorizedFailureThreshold(opts.OAuthTokenFetchFailureThreshold))
		var oauthTokenFetcher *auth.TokenFetcher
		if opts.OAuthClientSecretsEnvFile != "" {
			tokFetcher, err := auth.NewTokenFetcherFromFile(ctx,
				opts.TokenURL,
				opts.OAuthTokenScope,
				opts.OAuthClientID,
				opts.OAuthClientSecretsEnvFile,
				auth.WithResultListener(tokenFetcherHealthCheck),
				auth.WithScopeEnforcementEnabled(true),
				auth.WithEnvKey("OAUTH_CLIENT_SECRET_KEY"))
			if err != nil {
				core.GetLogger(ctx).WithError(err).Errorf("failed to retrieve OAuth token from file")
				return nil, nil, err
			}
			oauthTokenFetcher = tokFetcher
		} else {
			oauthTokenFetcher = auth.NewTokenFetcher(opts.TokenURL,
				opts.OAuthClientID,
				opts.OAuthClientSecretKey,
				opts.OAuthTokenScope,
				auth.WithResultListener(tokenFetcherHealthCheck),
				auth.WithScopeEnforcementEnabled(true))
		}
		jwksVerifier := cmnoauth.NewJWKSVerifier(opts.OAuthPublicKeysetEndpoint, cmnoauth.WithJWKSVerifierCacheTTL(DefaultJWKSKeySetTTL))
		return cmnoauth.NewJWTCache().
			WithFetcher(oauthTokenFetcher).
			WithVerifier(jwksVerifier).
			WithExpiryMargin(TokenExpiryMargin), tokenFetcherHealthCheck, nil
	} else if opts.NGCServiceAPIKeyFile != "" {
		name += "-ngc-key-tokenfetcher"
		keyFileFetcher, err := cmnsecret.NewKeyFileFetcher(ctx, cmnsecret.WithSecretKeyFile(opts.NGCServiceAPIKeyFile))
		return keyFileFetcher, health.SuccessfulTokenFetcherHealthCheck(name), err
	} else if opts.NGCServiceAPIKey != "" {
		name += "-ngc-key-tokenfetcher"
		keyFetcher, err := cmnsecret.NewKeyFileFetcher(ctx, cmnsecret.WithSecretKey(opts.NGCServiceAPIKey))
		return keyFetcher, health.SuccessfulTokenFetcherHealthCheck(name), err
	}

	return nil, nil, errors.New("an OAuth client ID, NGC Service API Key, or Self-Managed Vault Secrets JSON Path must be provided")
}
