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

package teardown

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlan_HelmUninstallCommand(t *testing.T) {
	opts := PlanOpts{
		Plane:       "control-plane",
		KubeContext: "admin@cp",
		StackPath:   "/stack",
		Releases: []ReleaseRef{
			{Name: "nvcf-api", Namespace: "nvcf-system"},
			{Name: "sis", Namespace: "sis-system"},
		},
	}
	plan, err := Plan(opts)
	require.NoError(t, err)

	require.Len(t, plan.WillUninstall, 2)

	// First release.
	r0 := plan.WillUninstall[0]
	assert.Equal(t, "helm", r0.Kind)
	assert.Equal(t, "nvcf-api", r0.Name)
	assert.Equal(t, "nvcf-system", r0.Namespace)
	assert.Equal(t, "helm uninstall nvcf-api -n nvcf-system --kube-context=admin@cp", r0.Command)

	// Second release.
	r1 := plan.WillUninstall[1]
	assert.Equal(t, "helm uninstall sis -n sis-system --kube-context=admin@cp", r1.Command)
}

func TestPlan_NoKubeContext(t *testing.T) {
	opts := PlanOpts{
		Plane: "control-plane",
		Releases: []ReleaseRef{
			{Name: "foo", Namespace: "bar"},
		},
	}
	plan, err := Plan(opts)
	require.NoError(t, err)

	require.Len(t, plan.WillUninstall, 1)
	assert.Equal(t, "helm uninstall foo -n bar", plan.WillUninstall[0].Command)
}

func TestPlan_TotalEstSec(t *testing.T) {
	opts := PlanOpts{
		Plane:    "control-plane",
		Releases: nil,
	}
	plan, err := Plan(opts)
	require.NoError(t, err)

	// Sum the embedded p50DownPhases table.
	var expected int
	for _, p := range p50DownPhases {
		expected += p.EstSec
	}
	assert.Equal(t, expected, plan.TotalEstSec)
	assert.Equal(t, len(p50DownPhases), len(plan.Phases))
}

func TestPlan_PhaseOrderIsPreserved(t *testing.T) {
	opts := PlanOpts{Plane: "control-plane"}
	plan, err := Plan(opts)
	require.NoError(t, err)

	for i, phase := range plan.Phases {
		assert.Equal(t, p50DownPhases[i].Num, phase.Num)
		assert.Equal(t, p50DownPhases[i].Name, phase.Name)
	}
}
