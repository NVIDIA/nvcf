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

package common

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractHelmConfiguration(t *testing.T) {
	type spec struct {
		name         string
		env          []string
		hcLaunchSpec *HelmChartLaunchSpecification
		expCfg       HelmConfig
		expErr       string
	}
	cases := []spec{
		{
			name:         "nil launch spec",
			env:          []string{},
			hcLaunchSpec: nil,
			expErr:       "empty Helm chart launch specification",
		},
		{
			name: "no env",
			env:  []string{},
			hcLaunchSpec: &HelmChartLaunchSpecification{
				HelmChartURL: "foobar",
			},
			expCfg: HelmConfig{
				URL:        "foobar",
				AuthConfig: HelmAuthConfig{},
			},
		},
		{
			name: "empty creds",
			env:  []string{"HELM_REGISTRIES_CREDENTIALS=" + base64.StdEncoding.EncodeToString([]byte(`{"k8sSecrets":[]}`))},
			hcLaunchSpec: &HelmChartLaunchSpecification{
				HelmChartURL: "foobar",
			},
			expCfg: HelmConfig{
				URL:        "foobar",
				AuthConfig: HelmAuthConfig{},
			},
		},
		{
			name:         "empty launch spec",
			env:          []string{"FOO=bar"},
			hcLaunchSpec: &HelmChartLaunchSpecification{},
			expErr:       "parse helm registry auth config: helm chart URL is empty",
		},
		{
			name: "good config",
			env:  []string{"HELM_REGISTRIES_CREDENTIALS=eyJrOHNTZWNyZXRzIjpbeyJhdXRocyI6eyJoZWxtLmV4YW1wbGUuY29tIjp7ImF1dGgiOiJKRzloZFhSb2RHOXJaVzQ2Ym5aaGNHa3RabTl2In0sImhlbG0ub3RoZXIuZXhhbXBsZS5jb20iOnsiYXV0aCI6ImIzUm9aWEoxYzJWeU9tOTBhR1Z5Y0dGemN3PT0ifSwiaGVsbS5zdGFnaW5nLmV4YW1wbGUuY29tIjp7ImF1dGgiOiJjM1JuTFdGaVl6RXlNenBtTWpWall6RmlPUzAxWWpGakxUUmhPREl0WVRWbU9DMDBPRGM1WWpVMll6VTVabU09In19fSx7ImF1dGhzIjp7Im9jaS5wcml2YXRlLmV4YW1wbGUuY29tOjUxMDAxIjp7ImF1dGgiOiJiMk5wY0hKcGRtRjBaWFZ6WlhJNmIyTnBjSEpwZG1GMFpYQmhjM009In19fSx7ImF1dGhzIjp7Im9jaS5wcml2YXRlLmV4YW1wbGUuY29tIjp7ImF1dGgiOiJiMk5wY0hKcGRtRjBaWFZ6WlhJeU9tOWphWEJ5YVhaaGRHVndZWE56TWc9PSJ9fX1dfQ=="},
			hcLaunchSpec: &HelmChartLaunchSpecification{
				HelmChartURL: "https://helm.staging.example.com/org/repo/charts/test-chart-0.0.1.tgz",
			},
			expCfg: HelmConfig{
				URL: "https://helm.staging.example.com/org/repo/charts/test-chart-0.0.1.tgz",
				AuthConfig: HelmAuthConfig{
					K8sSecrets: []RegistryAuthSecret{{
						Auths: map[string]RegistryAuth{
							"helm.staging.example.com": {
								Auth: "c3RnLWFiYzEyMzpmMjVjYzFiOS01YjFjLTRhODItYTVmOC00ODc5YjU2YzU5ZmM=",
							},
						},
					}},
				},
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sb := &strings.Builder{}
			for _, env := range tt.env {
				sb.WriteString(env)
				sb.WriteByte('\n')
			}
			envB64 := base64.StdEncoding.EncodeToString([]byte(sb.String()))
			gotHelmCfg, gotErr := ExtractHelmConfiguration(envB64, tt.hcLaunchSpec)
			if tt.expErr != "" {
				require.EqualError(t, gotErr, tt.expErr)
			} else {
				require.NoError(t, gotErr)
				assert.Equal(t, tt.expCfg, gotHelmCfg)
			}
		})
	}
}
