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

package dsl

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRenderSelfManagedSecretsUsesDockerCredentialFormat(t *testing.T) {
	apiKey := "test-api-key"
	template := []byte("first: " + selfManagedRegistryCredentialPlaceholder + "\nsecond: " + selfManagedRegistryCredentialPlaceholder + "\n")

	got, err := RenderSelfManagedSecrets(template, apiKey)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wantCredential := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:" + apiKey))
	if strings.Count(string(got), wantCredential) != 2 {
		t.Fatalf("rendered credential count = %d, want 2", strings.Count(string(got), wantCredential))
	}
	if strings.Contains(string(got), apiKey) {
		t.Fatal("raw API key leaked into rendered secrets")
	}
}

func TestRenderSelfManagedSecretsRejectsMissingAPIKey(t *testing.T) {
	_, err := RenderSelfManagedSecrets([]byte(selfManagedRegistryCredentialPlaceholder), "")
	if err == nil || !strings.Contains(err.Error(), "NGC_API_KEY is not set") {
		t.Fatalf("error = %v, want missing-key error", err)
	}
}

func TestRenderSelfManagedSecretsFailureHidesCredentialMaterial(t *testing.T) {
	apiKey := "sensitive-test-api-key"
	encoded := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:" + apiKey))

	_, err := RenderSelfManagedSecrets([]byte("registryCredential: missing\n"), apiKey)
	if err == nil {
		t.Fatal("expected missing-placeholder error")
	}
	for _, secret := range []string{apiKey, encoded} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked credential material: %v", err)
		}
	}
}
