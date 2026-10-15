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

package operator

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

// chartPath is the operator chart this test renders. Source of truth; the
// vendored copy under deploy/helm is generated from it.
const chartPath = "../../../deployments/nvca-operator"

// Test_NVCAAgentRBAC_OperatorCanDelegate asserts the operator holds every
// permission it grants to the agent.
//
// Kubernetes RBAC escalation prevention refuses a role that grants permissions
// the granting account does not itself hold. The operator builds the agent
// ClusterRole on every reconcile, so a rule added there without a matching
// rule in the chart-managed operator role breaks installation outright:
//
//	clusterroles "nvca" is forbidden: user "system:serviceaccount:
//	nvca-operator:nvca-operator" is attempting to grant RBAC permissions not
//	currently held: {APIGroups:["nvsnap.nvcf.nvidia.io"], ...}
//
// That check runs on the delegation itself, before any feature flag is read,
// so gating the *behaviour* behind a default-off flag does not prevent it --
// which is exactly how the NvSnapFunctionState rule shipped broken.
//
// This asserts the general invariant rather than the one rule, so the next
// resource added to the agent role is caught here instead of on a customer's
// clean install.
func Test_NVCAAgentRBAC_OperatorCanDelegate(t *testing.T) {
	agentRules := agentClusterRoleRules(t)
	operatorRules := renderOperatorClusterRoleRules(t)

	var missing []string
	for _, want := range agentRules {
		for _, group := range want.APIGroups {
			for _, resource := range want.Resources {
				if !granted(operatorRules, group, resource) {
					missing = append(missing,
						fmt.Sprintf("apiGroup=%q resource=%q", group, resource))
				}
			}
		}
	}
	require.Emptyf(t, missing,
		"the operator ClusterRole in %s must grant every permission the agent "+
			"ClusterRole delegates, or a clean install fails RBAC escalation "+
			"prevention.\nmissing: %s",
		chartPath, strings.Join(missing, "\n         "))
}

// granted reports whether the operator's rules cover this group/resource.
// A rule covers a resource when it lists it, or lists the "*" wildcard.
func granted(rules []rbacv1.PolicyRule, group, resource string) bool {
	for _, r := range rules {
		if !contains(r.APIGroups, group) && !contains(r.APIGroups, "*") {
			continue
		}
		if contains(r.Resources, resource) || contains(r.Resources, "*") {
			return true
		}
	}
	return false
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// agentClusterRoleRules drives the real reconcile path and reads back the
// ClusterRole it created, so the test always sees what production emits rather
// than a copy that can drift.
func agentClusterRoleRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{clients: clients, generateImagePullSecret: true}
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{NCAID: "1234-5678"},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterName:      "my-cluster",
					ClusterGroupName: "my-cluster-group",
					ClusterID:        "1234-45678",
					ClusterGroupID:   "1234-5678",
					CloudProvider:    "GCP",
					Region:           "us-west-1",
					LogLevel:         "info",
				},
			},
		},
	}
	require.NoError(t, bc.setupNVCARBAC(ctx, nb))

	cr, err := clients.K8s.RbacV1().ClusterRoles().
		Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cr.Rules, "agent ClusterRole has no rules")
	return cr.Rules
}

// renderOperatorClusterRoleRules renders the chart and returns the rules of
// the operator's own ClusterRole. Rendering rather than parsing the template
// keeps the test honest about what actually ships.
func renderOperatorClusterRoleRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not on PATH; skipping chart delegation check")
	}
	abs, err := filepath.Abs(chartPath)
	require.NoError(t, err)

	// ngcConfig.serviceKey is `required` by the chart's pull-secret helper.
	// Any non-empty value renders; nothing here depends on its contents.
	out, err := exec.Command(helm, "template", "nvca-operator", abs,
		"--set", "ngcConfig.serviceKey=test-key-not-a-secret",
		"--show-only", "templates/role.yaml").CombinedOutput()
	require.NoErrorf(t, err, "helm template failed: %s", out)

	var rules []rbacv1.PolicyRule
	for _, doc := range strings.Split(string(out), "\n---") {
		if !strings.Contains(doc, "kind: ClusterRole") {
			continue
		}
		var cr rbacv1.ClusterRole
		if err := yaml.Unmarshal([]byte(doc), &cr); err != nil {
			continue // not a ClusterRole document we can read; skip
		}
		// The chart also ships narrower helper roles; the operator's own role
		// is the one the operator ServiceAccount binds to.
		if cr.Kind == "ClusterRole" && !strings.Contains(cr.Name, "cluster-validator") {
			rules = append(rules, cr.Rules...)
		}
	}
	require.NotEmpty(t, rules, "no operator ClusterRole rules rendered from chart")
	return rules
}
