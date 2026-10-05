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

package storage

import (
	_ "embed"
	"fmt"
)

// builtinStorageCapabilityCatalogYAML is the catalog the NVCA chart ships as
// the nvcf-storage-capabilities ConfigMap, compiled into the agent so an agent
// whose chart has not converged resolves against the same defaults a fresh
// install would. The ConfigMap stays authoritative when it exists; this copy is
// read only when it is absent. TestBuiltinCatalogMatchesChart keeps the two
// identical.
//
//go:embed nvcf-storage-capabilities-v1alpha1.yaml
var builtinStorageCapabilityCatalogYAML string

// builtinStorageCapabilityCatalog parses the compiled-in catalog.
func builtinStorageCapabilityCatalog() (*storageCapabilityCatalog, string, error) {
	catalog, err := parseStorageCapabilityCatalog(builtinStorageCapabilityCatalogYAML)
	if err != nil {
		return nil, "", fmt.Errorf("built-in storage capability catalog: %w", err)
	}
	return catalog, digestCatalogPayload(builtinStorageCapabilityCatalogYAML), nil
}
