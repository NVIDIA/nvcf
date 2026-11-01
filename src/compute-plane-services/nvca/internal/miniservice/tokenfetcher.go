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

package mscontroller

import (
	"context"
	"os"
	"strings"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
)

const (
	//nolint:gosec // This is a valid URL
	stageJWKSURLEnvVarKey = "HELM_REVAL_STAGE_OAUTH_JWKS_URL"
	//nolint:gosec // This is a valid environment variable
	stageTokenURLEnvVarKey = "HELM_REVAL_STAGE_OAUTH_TOKEN_URL"

	//nolint:gosec // This is a valid environment variable
	prodJWKSURLEnvVarKey = "HELM_REVAL_PROD_OAUTH_JWKS_URL"
	//nolint:gosec // This is a valid environment variable
	prodTokenURLEnvVarKey = "HELM_REVAL_PROD_OAUTH_TOKEN_URL"

	//nolint:gosec // This is a valid environment variable
	oauthClientSecretKeyEnvVarKey = "HELM_REVAL_OAUTH_CLIENT_SECRET_KEY"
	//nolint:gosec // This is a valid environment variable
	oauthClientIDEnvVarKey = "HELM_REVAL_OAUTH_CLIENT_ID"
	//nolint:gosec // This is a valid environment variable
	oauthClientSecretsEnvFileEnvVarKey = "HELM_REVAL_OAUTH_CLIENT_SECRETS_ENV_FILE"

	//nolint:gosec // This is a valid environment variable
	ngcServiceAPIKeyFileEnvVarKey = "HELM_REVAL_NGC_SERVICE_API_KEY_FILE"
	//nolint:gosec // This is a valid environment variable
	ngcServiceAPIKeyEnvVarKey = "HELM_REVAL_NGC_SERVICE_API_KEY"
)

func NewTokenFetcher(ctx context.Context,
	opts nvcaauth.TokenFetcherOptions,
	revalEndpoint string,
) (nvcaauth.TokenFetcher, *health.TokenFetcherHealthCheck, error) {
	return nvcaauth.NewTokenFetcher(ctx, "reval", getHelmRevalTokenFetcherOptions(opts, revalEndpoint))
}

func getHelmRevalTokenFetcherOptions(opts nvcaauth.TokenFetcherOptions, revalEndpoint string) nvcaauth.TokenFetcherOptions {
	var tokenURL, jwksURL string

	// Determine if we're in stage or prod environment.
	isStage := strings.Contains(revalEndpoint, ".stg.") || strings.Contains(revalEndpoint, "://stg.")

	if isStage {
		tokenURL = os.Getenv(stageTokenURLEnvVarKey)
		jwksURL = os.Getenv(stageJWKSURLEnvVarKey)
	} else {
		tokenURL = os.Getenv(prodTokenURLEnvVarKey)
		jwksURL = os.Getenv(prodJWKSURLEnvVarKey)
	}
	if tokenURL == "" {
		tokenURL = opts.TokenURL
	}
	if jwksURL == "" {
		jwksURL = opts.OAuthPublicKeysetEndpoint
	}

	// Get the OAuth client ID and Secret Key from the environment variables, fallback to the options if not set
	oauthClientID := os.Getenv(oauthClientIDEnvVarKey)
	if oauthClientID == "" {
		oauthClientID = opts.OAuthClientID
	}
	oauthClientSecretKey := os.Getenv(oauthClientSecretKeyEnvVarKey)
	if oauthClientSecretKey == "" {
		oauthClientSecretKey = opts.OAuthClientSecretKey
	}
	oauthClientSecretsEnvFile := os.Getenv(oauthClientSecretsEnvFileEnvVarKey)
	if oauthClientSecretsEnvFile == "" {
		oauthClientSecretsEnvFile = opts.OAuthClientSecretsEnvFile
	}
	ngcServiceAPIKeyFile := os.Getenv(ngcServiceAPIKeyFileEnvVarKey)
	if ngcServiceAPIKeyFile == "" {
		ngcServiceAPIKeyFile = opts.NGCServiceAPIKeyFile
	}
	ngcServiceAPIKey := os.Getenv(ngcServiceAPIKeyEnvVarKey)
	if ngcServiceAPIKey == "" {
		ngcServiceAPIKey = opts.NGCServiceAPIKey
	}

	return nvcaauth.TokenFetcherOptions{
		SelfHostedEnabled:               opts.SelfHostedEnabled,
		SelfHostedVaultSecretsJSONPath:  opts.SelfHostedVaultSecretsJSONPath,
		PSATTokenFilePath:               opts.PSATTokenFilePath,
		OAuthClientID:                   oauthClientID,
		OAuthClientSecretKey:            oauthClientSecretKey,
		OAuthClientSecretsEnvFile:       oauthClientSecretsEnvFile,
		TokenURL:                        tokenURL,
		OAuthTokenScope:                 "helmreval:render",
		OAuthPublicKeysetEndpoint:       jwksURL,
		OAuthTokenFetchFailureThreshold: opts.OAuthTokenFetchFailureThreshold,
		NGCServiceAPIKeyFile:            ngcServiceAPIKeyFile,
		NGCServiceAPIKey:                ngcServiceAPIKey,
	}
}
