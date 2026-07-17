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
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	NVCAOTelCollectorAuthenticatorOAuth2Client    = "oauth2client"
	NVCAOTelCollectorAuthenticatorBearerTokenAuth = "bearertokenauth"

	NVCAOTelCollectorOAuthPlaceholderClientID = "nvca-otel-collector-client-id"
)

//go:embed manifests/otel_collector_config.yaml
var otelCollectorConfigTpl string

// otelCollectorAuthConfig holds the authentication configuration for the OTel collector
type otelCollectorAuthConfig struct {
	clientID         string
	clientSecretFile string
	tokenURL         string
	authenticator    string
}

// otelCollectorConfigTemplateData contains values used to render the OTel collector configuration template.
type otelCollectorConfigTemplateData struct {
	UseOAuth2 bool
}

// getOTelCollectorConfigData returns the OTel collector configuration data.
// The authentication extension is selected when the ConfigMap is rendered. Remaining
// values are substituted by the OTel Collector at runtime from the container environment.
func (bc *BackendK8sCache) getOTelCollectorConfigData(nb *nvidiaiov1.NVCFBackend) (map[string]string, error) {
	tmpl, err := template.New("otel_collector_config.yaml").Parse(otelCollectorConfigTpl)
	if err != nil {
		return nil, fmt.Errorf("parse OTel collector config template: %w", err)
	}

	var config bytes.Buffer
	if err := tmpl.Execute(&config, otelCollectorConfigTemplateData{UseOAuth2: useOTelCollectorOAuth2(nb)}); err != nil {
		return nil, fmt.Errorf("render OTel collector config template: %w", err)
	}

	return map[string]string{"config.yaml": config.String()}, nil
}

func useOTelCollectorOAuth2(nb *nvidiaiov1.NVCFBackend) bool {
	return nb.Spec.VaultConfig.Enabled && getOAuthConfig(nb).ClientID != ""
}

// getOTelCollectorAuthConfig determines the authentication configuration for the OTel collector.
// Falls back to NVCAOTelCollectorAuthenticatorBearerTokenAuth when Vault is disabled or client ID is empty.
func (bc *BackendK8sCache) getOTelCollectorAuthConfig(nb *nvidiaiov1.NVCFBackend) otelCollectorAuthConfig {
	clientID := NVCAOTelCollectorOAuthPlaceholderClientID
	vaultSecretFilePath := DefaultVaultSecretFilePath
	authenticator := NVCAOTelCollectorAuthenticatorBearerTokenAuth

	if useOTelCollectorOAuth2(nb) {
		if nb.Spec.VaultConfig.SecretFilePath != "" {
			vaultSecretFilePath = nb.Spec.VaultConfig.SecretFilePath
		}
		clientID = getOAuthConfig(nb).ClientID
		authenticator = NVCAOTelCollectorAuthenticatorOAuth2Client
	}

	tokenURL := ""
	if useOTelCollectorOAuth2(nb) {
		tokenURL = getFunctionDeploymentStagesOAuthTokenURL(nb, bc.envType)
		if tokenURL == "" {
			tokenURL = getOAuthConfig(nb).TokenURL
		}
	}
	clientSecretFile := getClientSecretsEnvFile(vaultSecretFilePath, nb.Spec.Version)

	return otelCollectorAuthConfig{
		clientID:         clientID,
		clientSecretFile: clientSecretFile,
		tokenURL:         tokenURL,
		authenticator:    authenticator,
	}
}

func getFunctionDeploymentStagesOAuthTokenURL(nb *nvidiaiov1.NVCFBackend, envType nvidiaiov1.EnvType) string {
	serviceURL := getFNDSEndpoint(nb.Spec.ClusterConfig.FNDService, envType)
	if useStageServiceOAuthEndpoints(serviceURL, envType) {
		return nb.Spec.AgentConfig.FunctionDeploymentStagesStageOAuthTokenURL
	}
	return nb.Spec.AgentConfig.FunctionDeploymentStagesProdOAuthTokenURL
}

func useStageServiceOAuthEndpoints(serviceURL string, envType nvidiaiov1.EnvType) bool {
	if strings.Contains(serviceURL, ".stg.") || strings.Contains(serviceURL, "://stg.") {
		return true
	}
	return envType == nvidiaiov1.EnvTypeStage
}

func getFNDSEndpoint(fndsCfg *nvidiaiov1.FNDServiceConfig, envType nvidiaiov1.EnvType) string {
	if fndsCfg != nil && fndsCfg.ServiceURL != "" {
		return fndsCfg.ServiceURL
	}
	// Fall back to default based on environment
	switch envType {
	case nvidiaiov1.EnvTypeStage:
		return nvidiaiov1.FunctionDeploymentStagesServiceURLStg
	default:
		return nvidiaiov1.FunctionDeploymentStagesServiceURLProd
	}
}
