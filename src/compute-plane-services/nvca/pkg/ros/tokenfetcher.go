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

package ros

import (
	"context"
	"os"
	"strings"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
)

const (
	//nolint:gosec // This is a valid environment variable
	stageJWKSURLEnvVarKey = "ROS_STAGE_OAUTH_JWKS_URL"
	//nolint:gosec // This is a valid environment variable
	stageTokenURLEnvVarKey = "ROS_STAGE_OAUTH_TOKEN_URL"
	//nolint:gosec // This is a valid environment variable
	prodJWKSURLEnvVarKey = "ROS_PROD_OAUTH_JWKS_URL"
	//nolint:gosec // This is a valid environment variable
	prodTokenURLEnvVarKey = "ROS_PROD_OAUTH_TOKEN_URL"
	//nolint:gosec // This is a valid environment variable
	oauthClientSecretKeyEnvVarKey = "ROS_OAUTH_CLIENT_SECRET_KEY"
	//nolint:gosec // This is a valid environment variable
	oauthClientIDEnvVarKey = "ROS_OAUTH_CLIENT_ID"
	//nolint:gosec // This is a valid environment variable
	oauthClientSecretsEnvFileEnvVarKey = "ROS_OAUTH_CLIENT_SECRETS_ENV_FILE"
	oauthClientScopeEnvVarKey          = "ROS_OAUTH_CLIENT_SCOPE"
	//nolint:gosec // This is a valid environment variable
	ngcServiceAPIKeyFileEnvVarKey = "ROS_NGC_SERVICE_API_KEY_FILE"
	//nolint:gosec // This is a valid environment variable
	ngcServiceAPIKeyEnvVarKey = "ROS_NGC_SERVICE_API_KEY"
)

func NewTokenFetcher(ctx context.Context,
	opts nvcaauth.TokenFetcherOptions,
	rosServiceURL string,
) (nvcaauth.TokenFetcher, *health.TokenFetcherHealthCheck, error) {
	return nvcaauth.NewTokenFetcher(ctx, "ros", getROSTokenFetcherOptions(opts, rosServiceURL))
}

func getROSTokenFetcherOptions(opts nvcaauth.TokenFetcherOptions, rosServiceURL string) nvcaauth.TokenFetcherOptions {
	var tokenURL, jwksURL string

	// Determine if we're in stage or prod environment
	// Handles both ".stg." in the middle (e.g., something.stg.nvidia.com) and
	// "://stg." at the start of hostname (e.g., https://stg.api.ros.nvidia.com)
	isStage := strings.Contains(rosServiceURL, ".stg.") || strings.Contains(rosServiceURL, "://stg.")

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
	oauthClientScope := os.Getenv(oauthClientScopeEnvVarKey)
	if oauthClientScope == "" {
		oauthClientScope = "instance_status"
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
		OAuthTokenScope:                 oauthClientScope,
		OAuthPublicKeysetEndpoint:       jwksURL,
		OAuthTokenFetchFailureThreshold: opts.OAuthTokenFetchFailureThreshold,
		NGCServiceAPIKeyFile:            ngcServiceAPIKeyFile,
		NGCServiceAPIKey:                ngcServiceAPIKey,
	}
}
