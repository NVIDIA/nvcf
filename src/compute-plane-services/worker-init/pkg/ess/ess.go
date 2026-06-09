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

package ess

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const (
	essConfigFileName                      = "config.hcl"
	essTemplateFileNameFunctionTaskSecrets = "secrets.tmpl"
	essTemplateFileNameAccountSecrets      = "accounts-secrets.tmpl"
	EssTokenFileName                       = "jwt.token"
)

type essConfigs struct {
	Address               string  `hcl:"address"`
	Namespace             string  `hcl:"namespace"`
	EssAgentTokenFile     string  `hcl:"ess_agent_token_file"`
	DefaultLeaseDuration  string  `hcl:"default_lease_duration"`
	LeaseRenewalThreshold float64 `hcl:"lease_renewal_threshold"`
}

type templateConfigs struct {
	Source      string `hcl:"source"`
	Destination string `hcl:"destination"`
}

type prometheusConfigs struct {
	TlsDisable bool   `hcl:"tls_disable"`
	Ip         string `hcl:"ip"`
	Port       int    `hcl:"port"`
}

type telemetryConfigs struct {
	PrometheusConfigs *prometheusConfigs `hcl:"prometheus,block"`
}

type essConfigHcl struct {
	Ess       *essConfigs       `hcl:"ess,block"`
	Templates []templateConfigs `hcl:"template,block"`
	Telemetry *telemetryConfigs `hcl:"telemetry,block"`
}

func SetupEssAgent(assertionToken, configDir, rawConfigDir string) error {
	if assertionToken == "" {
		zap.L().Info("Skip setting up ess agent as unable to find assertion token")
		return nil
	}

	zap.L().Info("Setting up ess agent")
	_, err := os.Stat(configDir)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(configDir, os.FileMode(0700))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	tokenFile := filepath.Join(configDir, EssTokenFileName)
	zap.L().Info("Writing assertion token", zap.String("token file", tokenFile))
	if err := os.WriteFile(tokenFile, []byte(assertionToken), 0600); err != nil {
		return err
	}

	var essConfigData essConfigHcl
	rawEssConfigFile := filepath.Join(rawConfigDir, essConfigFileName)
	fileContent, err := os.ReadFile(rawEssConfigFile)
	if err != nil {
		return err
	}

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCL(fileContent, "config.hcl")
	if diagnostics.HasErrors() {
		return diagnostics.Errs()[0]
	}

	diagnostics = gohcl.DecodeBody(file.Body, nil, &essConfigData)
	if diagnostics.HasErrors() {
		return diagnostics.Errs()[0]
	}
	if essFqdn := os.Getenv("ESS_FQDN"); essFqdn != "" {
		// gohcl leaves the pointer nil when the `ess` block is absent from
		// config.hcl; synthesize it so the env override still applies.
		if essConfigData.Ess == nil {
			essConfigData.Ess = &essConfigs{}
		}
		essConfigData.Ess.Address = essFqdn
	}
	// Without an ess block (and no ESS_FQDN to seed one) EncodeIntoBody
	// silently omits ess, yielding an agent with no address or token file.
	// Fail loudly instead of writing a broken config.
	if essConfigData.Ess == nil {
		return fmt.Errorf("ess block missing from config file %s", rawEssConfigFile)
	}

	f := hclwrite.NewEmptyFile()
	gohcl.EncodeIntoBody(essConfigData, f.Body())

	essConfigFile := filepath.Join(configDir, essConfigFileName)
	zap.L().Info("Copying config file to shared volume", zap.String("config file", essConfigFile))
	if err := os.WriteFile(essConfigFile, f.Bytes(), 0644); err != nil {
		return err
	}

	templateFiles := map[string]struct{}{
		essTemplateFileNameFunctionTaskSecrets: {},
		essTemplateFileNameAccountSecrets:      {},
	}

	for templateFile := range templateFiles {
		rawTemplateFile := filepath.Join(rawConfigDir, templateFile)
		templateFileFullPath := filepath.Join(configDir, templateFile)
		zap.L().Info("Copying template file to shared volume", zap.String("template file", templateFileFullPath))
		templateData, err := os.ReadFile(rawTemplateFile)
		if err != nil {
			return err
		}

		if err = os.WriteFile(templateFileFullPath, templateData, 0644); err != nil {
			return err
		}
	}
	return nil
}
