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

// Package crd embeds the CustomResourceDefinitions gds-mcs-controller needs:
// ServiceExport and ServiceImport from sigs.k8s.io/mcs-api config/crd and
// the KEP-2149 ClusterProperty kept in bases/. Tests install them into
// envtest; charts copy the YAML files.
package crd

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	mcscrd "sigs.k8s.io/mcs-api/config/crd"
)

// ClusterPropertyCRD is the ClusterProperty CRD (about.k8s.io/v1alpha1).
//
//go:embed bases/about.k8s.io_clusterproperties.yaml
var ClusterPropertyCRD []byte

// Files returns every CRD by file name.
func Files() map[string][]byte {
	return map[string][]byte{
		"multicluster.x-k8s.io_serviceexports.yaml": mcscrd.ServiceExportCRD,
		"multicluster.x-k8s.io_serviceimports.yaml": mcscrd.ServiceImportCRD,
		"about.k8s.io_clusterproperties.yaml":       ClusterPropertyCRD,
	}
}

// WriteDir writes every CRD into dir, for envtest's CRDDirectoryPaths.
func WriteDir(dir string) error {
	for name, data := range Files() {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("write CRD %s: %w", name, err)
		}
	}
	return nil
}
