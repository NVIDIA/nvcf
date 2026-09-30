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
	"bytes"
	"encoding/base64"
	"fmt"
)

const selfManagedRegistryCredentialPlaceholder = "REPLACE_WITH_BASE64_DOCKER_CREDENTIAL"

// RenderSelfManagedSecrets replaces the registry credential placeholder with
// base64 of the Docker username and current NGC API key. Errors never include
// the raw or encoded credential.
func RenderSelfManagedSecrets(template []byte, apiKey string) ([]byte, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("NGC_API_KEY is not set")
	}
	placeholder := []byte(selfManagedRegistryCredentialPlaceholder)
	if !bytes.Contains(template, placeholder) {
		return nil, fmt.Errorf("self-managed secrets template is missing the registry credential placeholder")
	}
	credential := base64.StdEncoding.EncodeToString([]byte("$oauthtoken:" + apiKey))
	return bytes.ReplaceAll(template, placeholder, []byte(credential)), nil
}
