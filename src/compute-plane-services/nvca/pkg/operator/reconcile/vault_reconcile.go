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
	_ "embed"
	"fmt"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	DefaultVaultNamespace        = "nvcf-bart"
	DefaultVaultServerAddress    = "https://:443"
	DefaultOAuthConfigRoleFmtStr = "k8s_%v_bart_jwt_role"
	DefaultJWTMountPathFmtStr    = "auth/jwt/k8s/%v"
	//nolint:gosec
	DefaultSecretDataPath = ".Data.data.secret"
)

func getVaultAnnotations(nb *nvidiaiov1.NVCFBackend) map[string]string {
	vaultSecretFilePath := DefaultVaultSecretFilePath
	if nb.Spec.VaultConfig.SecretFilePath != "" {
		vaultSecretFilePath = nb.Spec.VaultConfig.SecretFilePath
	}
	return map[string]string{
		"vault.hashicorp.com/agent-inject":        "true",
		"vault.hashicorp.com/agent-init-first":    "true",
		"vault.hashicorp.com/agent-inject-status": "update",
		"vault.hashicorp.com/agent-configmap":     NVCAVaultConfigmapName,
		"vault.hashicorp.com/secret-volume-path":  vaultSecretFilePath,
	}
}

//go:embed manifests/vault_config_template.hcl
var cfgTplFmt string

func getVaultConfigData(nb *nvidiaiov1.NVCFBackend) map[string]string {
	vaultNamespace := DefaultVaultNamespace
	vaultSrvAddress := DefaultVaultServerAddress
	vaultOAuthConfigRole := fmt.Sprintf(DefaultOAuthConfigRoleFmtStr, nb.Spec.ClusterConfig.ClusterName)
	secretDataPath := DefaultSecretDataPath
	vaultJWTMountPath := fmt.Sprintf(DefaultJWTMountPathFmtStr, nb.Spec.ClusterConfig.ClusterName)
	vaultSecretFilePathDir := DefaultVaultSecretFilePath

	// overrides for optional
	if nb.Spec.VaultConfig.VaultNamespace != "" {
		vaultNamespace = nb.Spec.VaultConfig.VaultNamespace
	}
	if nb.Spec.VaultConfig.Address != "" {
		vaultSrvAddress = nb.Spec.VaultConfig.Address
	}
	if nb.Spec.VaultConfig.OAuthConfigRole != "" {
		vaultOAuthConfigRole = nb.Spec.VaultConfig.OAuthConfigRole
	}
	if nb.Spec.VaultConfig.AuthMountPath != "" {
		vaultJWTMountPath = nb.Spec.VaultConfig.AuthMountPath
	}
	if nb.Spec.VaultConfig.SecretDataPath != "" {
		secretDataPath = nb.Spec.VaultConfig.SecretDataPath
	}
	if nb.Spec.VaultConfig.SecretFilePath != "" {
		vaultSecretFilePathDir = nb.Spec.VaultConfig.SecretFilePath
	}

	// Use version-aware vault file path (NVCA 2.51+ uses oauth-client-secrets.env, older uses oauth-client-secrets.env)
	vaultSecretFilePathDest := getClientSecretsEnvFile(vaultSecretFilePathDir, nb.Spec.Version)

	cfgTplInit := fmt.Sprintf(cfgTplFmt,
		vaultJWTMountPath,
		vaultNamespace,
		vaultOAuthConfigRole, "-init", "true",
		vaultSecretFilePathDest,
		vaultSrvAddress)

	cfgTplNonInit := fmt.Sprintf(cfgTplFmt,
		vaultJWTMountPath,
		vaultNamespace,
		vaultOAuthConfigRole, "", "false",
		vaultSecretFilePathDest,
		vaultSrvAddress)

	// Get OAuth mount path from CRD spec (set by helm chart or ngcclient)
	vaultSecretPath := getOAuthClientMountPath(nb)

	// Render the bare secret, no KEY= prefix: the otel collector's
	// oauth2client extension reads client_secret_file whole (no dotenv
	// parsing), so any prefix breaks its token fetch with 401. The agent's
	// fetcher parses both forms, and its client ID comes from config/env.
	temCfg := fmt.Sprintf("{{ with secret %q }}\n{{ %s }}\n{{ end }}",
		vaultSecretPath,
		secretDataPath)

	return map[string]string{
		"config-init.hcl": cfgTplInit,
		"config.hcl":      cfgTplNonInit,
		"template.hcl":    temCfg,
	}
}
