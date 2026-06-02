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
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	nvcrRegistry      = "nvcr.io"
	ngcDockerUsername = "$oauthtoken"
)

// NamespaceManifest returns a v1/Namespace YAML manifest body. The
// returned slice is the file contents the caller writes to disk and
// hands to kubectl apply.
func NamespaceManifest(namespace string) ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": namespace},
	})
}

// DockerConfigJSONSecretManifest returns a v1/Secret YAML manifest of
// type kubernetes.io/dockerconfigjson, with the API key embedded in
// the data.dockerconfigjson field. The key flows through the file
// body only; callers must not pass it on argv.
func DockerConfigJSONSecretManifest(secretName, namespace, apiKey string) ([]byte, error) {
	dockerConfig, err := json.Marshal(map[string]any{
		"auths": map[string]any{
			nvcrRegistry: map[string]any{
				"username": ngcDockerUsername,
				"password": apiKey,
				"auth":     base64.StdEncoding.EncodeToString([]byte(ngcDockerUsername + ":" + apiKey)),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal docker config: %w", err)
	}
	return yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": namespace,
		},
		"type": "kubernetes.io/dockerconfigjson",
		"data": map[string]any{
			".dockerconfigjson": base64.StdEncoding.EncodeToString(dockerConfig),
		},
	})
}
