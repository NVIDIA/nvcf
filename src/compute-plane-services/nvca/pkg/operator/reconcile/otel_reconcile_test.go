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

package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	otelCollectorTokenURLProd    = "https://fnds-oauth.example.test/token"
	otelCollectorTokenURLStage   = "https://stage-fnds-oauth.example.test/token"
	otelCollectorGenericTokenURL = "https://generic-oauth.example.test/token"
)

func TestGetFNDSEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		fndsCfg        *nvidiaiov1.FNDServiceConfig
		envType        nvidiaiov1.EnvType
		expectedResult string
	}{
		{
			name:           "Nil config - prod env - returns prod default",
			fndsCfg:        nil,
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLProd,
		},
		{
			name:           "Nil config - stage env - returns stage default",
			fndsCfg:        nil,
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLStg,
		},
		{
			name:           "Empty ServiceURL - prod env - returns prod default",
			fndsCfg:        &nvidiaiov1.FNDServiceConfig{},
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLProd,
		},
		{
			name:           "Empty ServiceURL - stage env - returns stage default",
			fndsCfg:        &nvidiaiov1.FNDServiceConfig{},
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLStg,
		},
		{
			name: "Custom ServiceURL - returns custom URL regardless of envType",
			fndsCfg: &nvidiaiov1.FNDServiceConfig{
				ServiceURL: "https://custom-fnds.example.com",
			},
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: "https://custom-fnds.example.com",
		},
		{
			name: "Custom ServiceURL with stage env - returns custom URL",
			fndsCfg: &nvidiaiov1.FNDServiceConfig{
				ServiceURL: "https://custom-fnds.example.com",
			},
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: "https://custom-fnds.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getFNDSEndpoint(tt.fndsCfg, tt.envType)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGetOTelCollectorConfigData(t *testing.T) {
	bc := &BackendK8sCache{}
	configData := bc.getOTelCollectorConfigData()

	require.Contains(t, configData, "config.yaml")
	config := configData["config.yaml"]

	// Verify environment variable placeholders are present in config (NVCA_OTEL_COLLECTOR_* naming)
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_REQUESTS_NAMESPACE}", "should contain requests namespace env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_MEMORY_LIMIT_PERCENTAGE}", "should contain memory limit percentage env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_SPIKE_LIMIT_PERCENTAGE}", "should contain spike limit percentage env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_HEALTH_CHECK_PORT}", "should contain health check port env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_FNDS_ENDPOINT}", "should contain FNDS endpoint env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_METRICS_PORT}", "should contain metrics port env var placeholder")

	// Verify OAuth-related env var placeholders are present
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_ID}", "should contain OAuth client ID env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_SECRET_FILE}", "should contain OAuth client secret file env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_OAUTH_TOKEN_URL}", "should contain OAuth token URL env var placeholder")
	assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_AUTHENTICATOR}", "should contain authenticator env var placeholder")
	assert.Contains(t, config, "${env:NGC_SERVICE_API_KEY_FILE}", "should contain NGC service API key file env var placeholder")

	// Verify key config sections are present
	assert.Contains(t, config, "memory_limiter", "should contain memory_limiter processor")
	assert.Contains(t, config, "k8s_events", "should contain k8s_events receiver")
	assert.Contains(t, config, "k8sattributes", "should contain k8sattributes processor")
	assert.Contains(t, config, "otlphttp", "should contain otlphttp exporter")
	assert.Contains(t, config, NVCAOTelCollectorAuthenticatorBearerTokenAuth, "should contain bearertokenauth extension")
	assert.Contains(t, config, NVCAOTelCollectorAuthenticatorOAuth2Client, "should contain oauth2client extension")
}

// otelAuthSpec builds minimal NVCFBackendSpecT for getOTelCollectorAuthConfig tests.
// empty value for vaultPath means use default; empty version means pre-2.51 (oauth-client-secrets.env).
func otelAuthSpec(vaultEnabled bool, clientID, prodTokenURL, stageTokenURL, version, vaultPath string) nvidiaiov1.NVCFBackendSpecT {
	spec := nvidiaiov1.NVCFBackendSpecT{
		VaultConfig: nvidiaiov1.VaultConfig{Enabled: vaultEnabled},
		OAuthConfig: nvidiaiov1.OAuthConfig{ClientID: clientID, TokenURL: otelCollectorGenericTokenURL},
		AgentConfig: nvidiaiov1.AgentConfig{
			FunctionDeploymentStagesProdOAuthTokenURL:  prodTokenURL,
			FunctionDeploymentStagesStageOAuthTokenURL: stageTokenURL,
		},
		Version: version,
	}
	if vaultPath != "" {
		spec.VaultConfig.SecretFilePath = vaultPath
	}
	return spec
}

func TestGetOTelCollectorAuthConfig(t *testing.T) {
	tests := []struct {
		name                  string
		nb                    *nvidiaiov1.NVCFBackend
		envType               nvidiaiov1.EnvType
		expectedClientID      string
		expectedSecretFile    string
		expectedTokenURL      string
		expectedAuthenticator string
	}{
		{
			name:                  "Vault enabled, prod, clientID set → OAuth2, oauth file",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid-prod", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "cid-prod",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, stage, clientID set → OAuth2, stage URL",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid-stage", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeStage,
			expectedClientID:      "cid-stage",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLStage,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, custom SecretFilePath",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "/custom/vault/path")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "cid",
			expectedSecretFile:    "/custom/vault/path/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, version 2.51+ → oauth-client-secrets.env",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "oauth-cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.53.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "oauth-cid",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, version 2.50 → oauth-client-secrets.env",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "oauth-cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.50.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "oauth-cid",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault disabled → bearer, placeholder",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(false, "", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      NVCAOTelCollectorOAuthPlaceholderClientID,
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
		{
			name:                  "Vault absent → bearer, placeholder, stage URL",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{}}},
			envType:               nvidiaiov1.EnvTypeStage,
			expectedClientID:      NVCAOTelCollectorOAuthPlaceholderClientID,
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
		{
			name:                  "Vault enabled, empty ClientID → fallback to bearer auth and placeholder",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.53.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      NVCAOTelCollectorOAuthPlaceholderClientID,
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{envType: tt.envType}
			result := bc.getOTelCollectorAuthConfig(tt.nb)
			assert.Equal(t, tt.expectedClientID, result.clientID)
			assert.Equal(t, tt.expectedSecretFile, result.clientSecretFile)
			assert.Equal(t, tt.expectedTokenURL, result.tokenURL)
			assert.Equal(t, tt.expectedAuthenticator, result.authenticator)
		})
	}
}
