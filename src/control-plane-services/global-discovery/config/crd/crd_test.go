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

package crd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDir(dir))
	want := map[string]string{
		"multicluster.x-k8s.io_serviceexports.yaml": "serviceexports.multicluster.x-k8s.io",
		"multicluster.x-k8s.io_serviceimports.yaml": "serviceimports.multicluster.x-k8s.io",
		"about.k8s.io_clusterproperties.yaml":       "clusterproperties.about.k8s.io",
	}
	for file, name := range want {
		data, err := os.ReadFile(filepath.Join(dir, file))
		require.NoError(t, err)
		var crd struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Scope string `json:"scope"`
			} `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal(data, &crd), file)
		assert.Equal(t, "CustomResourceDefinition", crd.Kind)
		assert.Equal(t, name, crd.Metadata.Name)
		if file == "about.k8s.io_clusterproperties.yaml" {
			assert.Equal(t, "Cluster", crd.Spec.Scope)
			assert.NotEmpty(t, crd.Metadata.Annotations["api-approved.kubernetes.io"], "about.k8s.io is a protected group")
		} else {
			assert.Equal(t, "Namespaced", crd.Spec.Scope)
		}
	}
	require.Error(t, WriteDir(filepath.Join(dir, "missing")))
}
