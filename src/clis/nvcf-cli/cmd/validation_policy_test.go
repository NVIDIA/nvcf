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

package cmd

import (
	"testing"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- splitExtraType ---

func TestSplitExtraType(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		expect  []string
		wantErr string
	}{
		{name: "valid three parts", raw: "apps/v1/Deployment", want: 3, expect: []string{"apps", "v1", "Deployment"}},
		{name: "valid four parts", raw: "apps/v1/Deployment/deployments", want: 4, expect: []string{"apps", "v1", "Deployment", "deployments"}},
		{name: "too few parts", raw: "apps/v1", want: 3, wantErr: "expected group/version/kind (3 parts, got 2)"},
		{name: "too many parts", raw: "apps/v1/Deployment/deployments", want: 3, wantErr: "expected group/version/kind (3 parts, got 4)"},
		{name: "empty middle part", raw: "apps//Deployment", want: 3, wantErr: "part 2 is empty"},
		{name: "whitespace-only part", raw: "apps/ /Deployment", want: 3, wantErr: "part 2 is empty"},
		{name: "trailing slash leaves empty part", raw: "apps/v1/Deployment/", want: 4, wantErr: "part 4 is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitExtraType(tt.raw, tt.want)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expect, got)
		})
	}
}

// --- parseWorkloadExtraType / parseClusterExtraType ---

func TestParseWorkloadExtraType(t *testing.T) {
	t.Run("valid group/version/kind", func(t *testing.T) {
		kt, err := parseWorkloadExtraType("leaderworkerset.x-k8s.io/v1/LeaderWorkerSet")
		require.NoError(t, err)
		assert.Equal(t, client.KubernetesType{
			Group:   "leaderworkerset.x-k8s.io",
			Version: "v1",
			Kind:    "LeaderWorkerSet",
		}, kt)
		// The workload side must never carry the resource (plural) name.
		assert.Empty(t, kt.Resource)
	})

	t.Run("rejects four-part cluster form", func(t *testing.T) {
		_, err := parseWorkloadExtraType("apps/v1/Deployment/deployments")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group/version/kind")
	})
}

func TestParseClusterExtraType(t *testing.T) {
	t.Run("valid group/version/kind/resource", func(t *testing.T) {
		kt, err := parseClusterExtraType("leaderworkerset.x-k8s.io/v1/LeaderWorkerSet/leaderworkersets")
		require.NoError(t, err)
		assert.Equal(t, client.KubernetesType{
			Group:    "leaderworkerset.x-k8s.io",
			Version:  "v1",
			Kind:     "LeaderWorkerSet",
			Resource: "leaderworkersets",
		}, kt)
	})

	t.Run("rejects three-part workload form", func(t *testing.T) {
		_, err := parseClusterExtraType("apps/v1/Deployment")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group/version/kind/resource")
	})
}

// --- buildWorkloadValidationPolicy ---

func TestBuildWorkloadValidationPolicy(t *testing.T) {
	t.Run("no flags returns existing unchanged", func(t *testing.T) {
		existing := &client.HelmValidationPolicyDto{Name: "Unrestricted"}
		got, err := buildWorkloadValidationPolicy(existing, "", false, nil, false)
		require.NoError(t, err)
		assert.Same(t, existing, got)
	})

	t.Run("no flags with nil existing returns nil", func(t *testing.T) {
		got, err := buildWorkloadValidationPolicy(nil, "", false, nil, false)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("no flags normalizes unnamed file policy to Default", func(t *testing.T) {
		existing := &client.HelmValidationPolicyDto{
			ExtraKubernetesTypes: []client.KubernetesType{{Group: "apps", Version: "v1", Kind: "Deployment"}},
		}
		got, err := buildWorkloadValidationPolicy(existing, "", false, nil, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, defaultValidationPolicyName, got.Name)
		assert.Equal(t, []client.KubernetesType{{Group: "apps", Version: "v1", Kind: "Deployment"}}, got.ExtraKubernetesTypes)
	})

	t.Run("name only over nil existing", func(t *testing.T) {
		got, err := buildWorkloadValidationPolicy(nil, "Unrestricted", true, nil, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Empty(t, got.ExtraKubernetesTypes)
	})

	t.Run("types only defaults name to Default", func(t *testing.T) {
		got, err := buildWorkloadValidationPolicy(nil, "", false, []string{"apps/v1/Deployment"}, true)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, defaultValidationPolicyName, got.Name)
		assert.Equal(t, []client.KubernetesType{{Group: "apps", Version: "v1", Kind: "Deployment"}}, got.ExtraKubernetesTypes)
	})

	t.Run("both flags set", func(t *testing.T) {
		got, err := buildWorkloadValidationPolicy(nil, "Unrestricted", true,
			[]string{"apps/v1/Deployment", "batch/v1/Job"}, true)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Equal(t, []client.KubernetesType{
			{Group: "apps", Version: "v1", Kind: "Deployment"},
			{Group: "batch", Version: "v1", Kind: "Job"},
		}, got.ExtraKubernetesTypes)
	})

	t.Run("name flag overrides file, types replace file list", func(t *testing.T) {
		existing := &client.HelmValidationPolicyDto{
			Name:                 "Default",
			ExtraKubernetesTypes: []client.KubernetesType{{Group: "old", Version: "v1", Kind: "Thing"}},
		}
		got, err := buildWorkloadValidationPolicy(existing, "Unrestricted", true,
			[]string{"apps/v1/Deployment"}, true)
		require.NoError(t, err)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Equal(t, []client.KubernetesType{{Group: "apps", Version: "v1", Kind: "Deployment"}}, got.ExtraKubernetesTypes)
	})

	t.Run("empty name flag defaults to Default", func(t *testing.T) {
		got, err := buildWorkloadValidationPolicy(nil, "", true, nil, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, defaultValidationPolicyName, got.Name)
	})

	t.Run("clearing types keeps file name but empties list", func(t *testing.T) {
		existing := &client.HelmValidationPolicyDto{
			Name:                 "Unrestricted",
			ExtraKubernetesTypes: []client.KubernetesType{{Group: "old", Version: "v1", Kind: "Thing"}},
		}
		got, err := buildWorkloadValidationPolicy(existing, "", false, nil, true)
		require.NoError(t, err)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Empty(t, got.ExtraKubernetesTypes)
	})

	t.Run("invalid extra type propagates error", func(t *testing.T) {
		_, err := buildWorkloadValidationPolicy(nil, "", false, []string{"apps/v1/Deployment/deployments"}, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group/version/kind")
	})
}

// --- buildClusterValidationPolicy ---

func newClusterPolicyTestCmd(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "register"}
	cmd.Flags().String(flagValidationPolicy, "", "")
	cmd.Flags().StringArray(flagValidationExtraType, nil, "")
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

func TestBuildClusterValidationPolicy(t *testing.T) {
	t.Run("no flags returns nil policy", func(t *testing.T) {
		cmd := newClusterPolicyTestCmd(t, nil)
		got, err := buildClusterValidationPolicy(cmd)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("name only", func(t *testing.T) {
		cmd := newClusterPolicyTestCmd(t, []string{"--" + flagValidationPolicy, "Unrestricted"})
		got, err := buildClusterValidationPolicy(cmd)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Empty(t, got.AllowedExtraKubernetesTypes)
	})

	t.Run("types only defaults name to Default and requires resource", func(t *testing.T) {
		cmd := newClusterPolicyTestCmd(t, []string{
			"--" + flagValidationExtraType, "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet/leaderworkersets",
		})
		got, err := buildClusterValidationPolicy(cmd)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, defaultValidationPolicyName, got.Name)
		assert.Equal(t, []client.KubernetesType{{
			Group:    "leaderworkerset.x-k8s.io",
			Version:  "v1",
			Kind:     "LeaderWorkerSet",
			Resource: "leaderworkersets",
		}}, got.AllowedExtraKubernetesTypes)
	})

	t.Run("name and multiple types", func(t *testing.T) {
		cmd := newClusterPolicyTestCmd(t, []string{
			"--" + flagValidationPolicy, "Unrestricted",
			"--" + flagValidationExtraType, "apps/v1/Deployment/deployments",
			"--" + flagValidationExtraType, "batch/v1/Job/jobs",
		})
		got, err := buildClusterValidationPolicy(cmd)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Unrestricted", got.Name)
		assert.Equal(t, []client.KubernetesType{
			{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments"},
			{Group: "batch", Version: "v1", Kind: "Job", Resource: "jobs"},
		}, got.AllowedExtraKubernetesTypes)
	})

	t.Run("cluster type missing resource is rejected", func(t *testing.T) {
		cmd := newClusterPolicyTestCmd(t, []string{
			"--" + flagValidationExtraType, "apps/v1/Deployment",
		})
		_, err := buildClusterValidationPolicy(cmd)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group/version/kind/resource")
	})
}

// --- ignoreExistingPolicyConflict ---

func TestIgnoreExistingPolicyConflict(t *testing.T) {
	t.Run("nil policy is allowed", func(t *testing.T) {
		assert.NoError(t, ignoreExistingPolicyConflict("cl-1", nil))
	})

	t.Run("non-nil policy is rejected with flag names and cluster", func(t *testing.T) {
		policy := &client.ClusterHelmValidationPolicy{Name: "Unrestricted"}
		err := ignoreExistingPolicyConflict("cl-1", policy)
		require.Error(t, err)
		assert.Contains(t, err.Error(), flagValidationPolicy)
		assert.Contains(t, err.Error(), flagValidationExtraType)
		assert.Contains(t, err.Error(), "cl-1")
	})
}

// --- applyTaskValidationPolicyFlags ---

func newTaskPolicyTestCmd(t *testing.T, args []string) *cobra.Command {
	t.Helper()
	// Bind to the same globals the production code reads, then restore them so
	// no test state leaks into other cmd tests that touch taskCreateFlags.
	prevName := taskCreateFlags.validationPolicy
	prevTypes := taskCreateFlags.validationExtraType
	t.Cleanup(func() {
		taskCreateFlags.validationPolicy = prevName
		taskCreateFlags.validationExtraType = prevTypes
	})
	taskCreateFlags.validationPolicy = ""
	taskCreateFlags.validationExtraType = nil

	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().StringVar(&taskCreateFlags.validationPolicy, flagValidationPolicy, "", "")
	cmd.Flags().StringArrayVar(&taskCreateFlags.validationExtraType, flagValidationExtraType, nil, "")
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

func TestApplyTaskValidationPolicyFlags(t *testing.T) {
	t.Run("name only over empty spec", func(t *testing.T) {
		cmd := newTaskPolicyTestCmd(t, []string{"--" + flagValidationPolicy, "Unrestricted"})
		spec := &TaskGpuSpecificationInput{}
		require.NoError(t, applyTaskValidationPolicyFlags(cmd, spec))
		require.NotNil(t, spec.HelmValidationPolicy)
		assert.Equal(t, "Unrestricted", spec.HelmValidationPolicy.Name)
		assert.Empty(t, spec.HelmValidationPolicy.ExtraKubernetesTypes)
	})

	t.Run("types only defaults name and uses three-part types", func(t *testing.T) {
		cmd := newTaskPolicyTestCmd(t, []string{
			"--" + flagValidationExtraType, "apps/v1/Deployment",
		})
		spec := &TaskGpuSpecificationInput{}
		require.NoError(t, applyTaskValidationPolicyFlags(cmd, spec))
		require.NotNil(t, spec.HelmValidationPolicy)
		assert.Equal(t, defaultValidationPolicyName, spec.HelmValidationPolicy.Name)
		assert.Equal(t, []TaskKubernetesTypeIn{{Group: "apps", Version: "v1", Kind: "Deployment"}},
			spec.HelmValidationPolicy.ExtraKubernetesTypes)
	})

	t.Run("flags merge over file policy: name overrides, types replace", func(t *testing.T) {
		cmd := newTaskPolicyTestCmd(t, []string{
			"--" + flagValidationPolicy, "Unrestricted",
			"--" + flagValidationExtraType, "batch/v1/Job",
		})
		spec := &TaskGpuSpecificationInput{
			HelmValidationPolicy: &TaskHelmValidationInput{
				Name:                 "Default",
				ExtraKubernetesTypes: []TaskKubernetesTypeIn{{Group: "old", Version: "v1", Kind: "Thing"}},
			},
		}
		require.NoError(t, applyTaskValidationPolicyFlags(cmd, spec))
		assert.Equal(t, "Unrestricted", spec.HelmValidationPolicy.Name)
		assert.Equal(t, []TaskKubernetesTypeIn{{Group: "batch", Version: "v1", Kind: "Job"}},
			spec.HelmValidationPolicy.ExtraKubernetesTypes)
	})

	t.Run("cluster-style four-part type is rejected", func(t *testing.T) {
		cmd := newTaskPolicyTestCmd(t, []string{
			"--" + flagValidationExtraType, "apps/v1/Deployment/deployments",
		})
		spec := &TaskGpuSpecificationInput{}
		err := applyTaskValidationPolicyFlags(cmd, spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "group/version/kind")
	})

	t.Run("no flags normalizes unnamed file policy to Default", func(t *testing.T) {
		cmd := newTaskPolicyTestCmd(t, nil)
		spec := &TaskGpuSpecificationInput{
			HelmValidationPolicy: &TaskHelmValidationInput{
				ExtraKubernetesTypes: []TaskKubernetesTypeIn{{Group: "apps", Version: "v1", Kind: "Deployment"}},
			},
		}
		require.NoError(t, applyTaskValidationPolicyFlags(cmd, spec))
		require.NotNil(t, spec.HelmValidationPolicy)
		assert.Equal(t, defaultValidationPolicyName, spec.HelmValidationPolicy.Name)
		assert.Equal(t, []TaskKubernetesTypeIn{{Group: "apps", Version: "v1", Kind: "Deployment"}},
			spec.HelmValidationPolicy.ExtraKubernetesTypes)
	})
}
