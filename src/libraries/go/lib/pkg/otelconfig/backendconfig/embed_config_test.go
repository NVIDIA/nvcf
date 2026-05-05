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

package backendconfig

import (
	"bytes"
	"testing"
)

func TestExecuteTemplate(t *testing.T) {
	tests := []struct {
		name         string
		tcfg         TemplateConfig
		expectError  bool
		expectedFile string
	}{
		{
			name: "VM Container",
			tcfg: TemplateConfig{
				BackendType:  VM,
				WorkloadType: Container,
			},
			expectError:  false,
			expectedFile: "config-vm-container.yaml.tmpl",
		},
		{
			name: "VM Helm",
			tcfg: TemplateConfig{
				BackendType:  VM,
				WorkloadType: Helm,
			},
			expectError:  false,
			expectedFile: "config-vm-helm.yaml.tmpl",
		},
		{
			name: "K8s",
			tcfg: TemplateConfig{
				BackendType:  K8s,
				WorkloadType: Container,
			},
			expectError:  false,
			expectedFile: "config-k8s-container.yaml.tmpl",
		},
		{
			name: "K8s",
			tcfg: TemplateConfig{
				BackendType:  K8s,
				WorkloadType: Helm,
			},
			expectError:  false,
			expectedFile: "config-k8s-helm.yaml.tmpl",
		},
		{
			name: "Unknown Backend Type",
			tcfg: TemplateConfig{
				BackendType:  "Unknown",
				WorkloadType: Container,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := ExecuteTemplate(&buf, tt.tcfg)
			if (err != nil) != tt.expectError {
				t.Errorf("ExecuteTemplate() error = %v, expectError %v", err, tt.expectError)
			}
			if !tt.expectError {
				if buf.String() == "" {
					t.Errorf("Expected template output, got none")
				}
			}
		})
	}
}
