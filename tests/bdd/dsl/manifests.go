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
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	nvcrRegistry      = "nvcr.io"
	ngcDockerUsername = "$oauthtoken"
)

// RenderedManifestsContainResource parses rendered YAML documents below root
// and requires one top-level Kubernetes resource with the requested kind and
// metadata.name. Nested references such as Certificate.spec.issuerRef do not
// satisfy the assertion.
func RenderedManifestsContainResource(root string, resource KubernetesResource) error {
	root = strings.TrimSpace(Interpolate(root))
	resource.Kind = strings.TrimSpace(Interpolate(resource.Kind))
	resource.Name = strings.TrimSpace(Interpolate(resource.Name))
	if root == "" {
		return fmt.Errorf("rendered manifests directory is empty")
	}
	if resource.Kind == "" {
		return fmt.Errorf("kubernetes resource kind is empty")
	}
	if resource.Name == "" {
		return fmt.Errorf("kubernetes resource name is empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("inspect rendered manifests directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("rendered manifests path %q is not a directory", root)
	}

	yamlFilesInspected := 0
	found := false
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect rendered manifest %q: %w", filePath, walkErr)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(filePath))
		if extension != ".yaml" && extension != ".yml" {
			return nil
		}
		yamlFilesInspected++

		manifestBody, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read rendered manifest %q: %w", filePath, err)
		}

		decoder := yaml.NewDecoder(bytes.NewReader(manifestBody))
		for document := 1; ; document++ {
			var manifest struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
			}
			if err := decoder.Decode(&manifest); err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("parse rendered manifest %q document %d: invalid YAML", filePath, document)
			}
			if manifest.Kind == resource.Kind && manifest.Metadata.Name == resource.Name {
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if yamlFilesInspected == 0 {
		return fmt.Errorf("rendered manifests directory %q contains no YAML files", root)
	}
	return fmt.Errorf("rendered manifests in %q do not contain Kubernetes resource %s/%s", root, resource.Kind, resource.Name)
}

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
