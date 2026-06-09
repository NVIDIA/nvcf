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
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

func TestEssSetup(t *testing.T) {
	targetDir := t.TempDir()
	mockToken := "mock-token"

	if err := SetupEssAgent(mockToken, targetDir, "configs"); err != nil {
		t.Fatal(err)
	}

	token, err := os.ReadFile(filepath.Join(targetDir, EssTokenFileName))
	if err != nil {
		t.Fatal(err)
	}

	if string(token) != mockToken {
		t.Fatalf("get token: %s, want token: %s", token, mockToken)
	}

	templateFiles := map[string]struct{}{
		essTemplateFileNameFunctionTaskSecrets: {},
		essTemplateFileNameAccountSecrets:      {},
	}

	for templateFile := range templateFiles {
		template, err := os.ReadFile(filepath.Join(targetDir, templateFile))
		if err != nil {
			t.Fatal(err)
		}

		rawTemplate, err := os.ReadFile(filepath.Join("configs", templateFile))
		if err != nil {
			t.Fatal(err)
		}
		if string(template) != string(rawTemplate) {
			t.Fatalf("get wrong template file %s", templateFile)
		}
	}

	var essConfigs essConfigHcl
	if err := hclsimple.DecodeFile(filepath.Join(targetDir, essConfigFileName), nil, &essConfigs); err != nil {
		t.Fatal(err)
	}

	if err := validateEssConfigs(essConfigs); err != nil {
		t.Fatal(err)
	}
}

func validateEssConfigs(essConfigs essConfigHcl) error {
	if essConfigs.Ess.Address != "http://localhost:8200" {
		return errors.New("get wrong ess address")
	}

	if essConfigs.Ess.Namespace != "nvcf" {
		return errors.New("get wrong namespace")
	}

	if essConfigs.Ess.EssAgentTokenFile != "/config/ess-agent/jwt.token" {
		return errors.New("get wrong ess agent token file")
	}

	if essConfigs.Ess.DefaultLeaseDuration != "15m" || essConfigs.Ess.LeaseRenewalThreshold != 0.8 {
		return errors.New("get wrong lease configs")
	}

	if essConfigs.Templates[0].Destination != "/var/secrets/secrets.json" || essConfigs.Templates[0].Source != "/config/ess-agent/secrets.tmpl" {
		return errors.New("get wrong template configs")
	}

	if essConfigs.Templates[1].Destination != "/var/secrets/accounts-secrets.json" || essConfigs.Templates[1].Source != "/config/ess-agent/accounts-secrets.tmpl" {
		return errors.New("get wrong template configs for account secrets")
	}

	prometheusConfigs := essConfigs.Telemetry.PrometheusConfigs
	if !prometheusConfigs.TlsDisable || prometheusConfigs.Ip != "0.0.0.0" || prometheusConfigs.Port != 10103 {
		return errors.New("get wrong prometheus configs")
	}

	return nil
}
