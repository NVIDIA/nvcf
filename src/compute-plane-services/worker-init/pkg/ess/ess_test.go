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
	"strings"
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

func TestSetupEssAgentSkipsOnEmptyToken(t *testing.T) {
	dir := t.TempDir()
	if err := SetupEssAgent("", dir, "configs"); err != nil {
		t.Fatalf("unexpected error with empty token: %v", err)
	}
	_, err := os.Stat(filepath.Join(dir, EssTokenFileName))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("token file must not be created when assertion token is empty")
	}
}

func TestSetupEssAgentEssFqdnOverride(t *testing.T) {
	const override = "https://ess.override.example.com"
	t.Setenv("ESS_FQDN", override)

	targetDir := t.TempDir()
	if err := SetupEssAgent("tok", targetDir, "configs"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var cfg essConfigHcl
	if err := hclsimple.DecodeFile(filepath.Join(targetDir, essConfigFileName), nil, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Ess == nil || cfg.Ess.Address != override {
		got := ""
		if cfg.Ess != nil {
			got = cfg.Ess.Address
		}
		t.Fatalf("expected ESS address %q after ESS_FQDN override, got %q", override, got)
	}
}

func TestSetupEssAgentCreatesMissingConfigDir(t *testing.T) {
	// configDir does not exist yet, exercising the CreateDirectory branch.
	configDir := filepath.Join(t.TempDir(), "ess-agent")
	if err := SetupEssAgent("tok", configDir, "configs"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, EssTokenFileName)); err != nil {
		t.Fatalf("expected token file to be written into the created dir: %v", err)
	}
}

func TestSetupEssAgentMissingTemplateError(t *testing.T) {
	// rawConfigDir has a valid config.hcl with an ess block but is missing the
	// template files, so the template-copy step fails.
	rawDir := t.TempDir()
	hcl := `ess {
  address = "http://localhost:8200"
  namespace = "nvcf"
  ess_agent_token_file = "/config/ess-agent/jwt.token"
  default_lease_duration = "15m"
  lease_renewal_threshold = 0.80
}`
	if err := os.WriteFile(filepath.Join(rawDir, essConfigFileName), []byte(hcl), 0600); err != nil {
		t.Fatal(err)
	}

	err := SetupEssAgent("tok", t.TempDir(), rawDir)
	if err == nil {
		t.Fatal("expected error when template files are missing from raw config dir")
	}
}

func TestSetupEssAgentMissingConfigFileError(t *testing.T) {
	// rawConfigDir has no config.hcl at all; the read must fail.
	err := SetupEssAgent("tok", t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected error when config.hcl is missing from raw config dir")
	}
}

func TestSetupEssAgentNilEssBlockError(t *testing.T) {
	rawDir := t.TempDir()
	hcl := `template {
  source      = "/config/ess-agent/secrets.tmpl"
  destination = "/var/secrets/secrets.json"
}`
	if err := os.WriteFile(filepath.Join(rawDir, essConfigFileName), []byte(hcl), 0600); err != nil {
		t.Fatal(err)
	}

	err := SetupEssAgent("tok", t.TempDir(), rawDir)
	if err == nil {
		t.Fatal("expected error when ess block is missing from HCL, got nil")
	}
	if !strings.Contains(err.Error(), "ess block missing") {
		t.Fatalf("expected 'ess block missing' in error, got: %v", err)
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
