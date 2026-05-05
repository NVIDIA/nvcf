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
	"encoding/json"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

// celMatchCondEnv builds a CEL environment that mirrors the variables the
// Kubernetes API server provides when evaluating webhook matchConditions:
//
//   - request  (admission request; has .kind.kind, .name, .namespace, …)
//   - object   (the admitted object; has .metadata.ownerReferences, …)
//   - oldObject (previous version for UPDATE; unused here, included for parity)
//
// All three are declared as DynType because the API server passes them as
// unstructured JSON maps.
func celMatchCondEnv() *cel.Env {
	env, err := cel.NewEnv(
		cel.Variable("request", cel.DynType),
		cel.Variable("object", cel.DynType),
		cel.Variable("oldObject", cel.DynType),
	)
	if err != nil {
		panic(err)
	}
	return env
}

// evalCEL compiles expr in the matchCondition environment and evaluates it
// against the supplied variable bindings. Returns the boolean result.
func evalCEL(t *testing.T, env *cel.Env, expr string, vars map[string]any) bool {
	t.Helper()
	ast, issues := env.Compile(expr)
	require.NoError(t, issues.Err(), "CEL compile failed for %q", expr)
	prg, err := env.Program(ast)
	require.NoError(t, err, "CEL program creation failed")
	out, _, err := prg.Eval(vars)
	require.NoError(t, err, "CEL eval failed")
	return out.Value().(bool)
}

// makeRequestVar returns the unstructured map the K8s API server would pass as
// the "request" CEL variable for a webhook matchCondition evaluation.
func makeRequestVar(t *testing.T, kind, name, namespace string) map[string]any {
	t.Helper()
	req := admissionv1.AdmissionRequest{
		Kind: metav1.GroupVersionKind{
			Group:   "",
			Version: "v1",
			Kind:    kind,
		},
		Name:      name,
		Namespace: namespace,
	}
	reqData, err := json.Marshal(req)
	require.NoError(t, err)
	var reqMap map[string]any
	require.NoError(t, json.Unmarshal(reqData, &reqMap))
	return reqMap
}

// makeObjectVar returns the unstructured map the K8s API server would pass as
// the "object" CEL variable. ownerRefs is a slice of {kind, name} maps.
func makeObjectVar(ownerRefs []map[string]any) map[string]any {
	if ownerRefs == nil {
		return map[string]any{
			"metadata": map[string]any{},
		}
	}
	return map[string]any{
		"metadata": map[string]any{
			"ownerReferences": ownerRefs,
		},
	}
}

func ownerRef(kind, name string) map[string]any {
	return map[string]any{"kind": kind, "name": name}
}

// TestMiniServiceMutatingWebhook_MatchConditions evaluates the CEL
// matchCondition expressions using the cel-go library, exercising the same
// code path the Kubernetes API server uses at admission time.
func TestMiniServiceMutatingWebhook_MatchConditions(t *testing.T) {
	bc := &BackendK8sCache{}
	nb := &nvidiaiov1.NVCFBackend{}
	whs := bc.makeMiniServiceMutatingWebhooks(nb, WebhookCert{})

	require.Len(t, whs, 2, "expected CREATE and UPDATE webhooks")
	for _, wh := range whs {
		require.Len(t, wh.MatchConditions, 2, "each webhook must have 2 match conditions")
	}

	matchConds := whs[0].MatchConditions
	env := celMatchCondEnv()

	t.Run("exclude-smb-server-pod", func(t *testing.T) {
		expr := matchConds[0].Expression

		tests := []struct {
			name      string
			request   map[string]any
			wantMatch bool
		}{
			{
				name:      "regular workload pod matches",
				request:   makeRequestVar(t, "Pod", "my-workload-pod", "ns1"),
				wantMatch: true,
			},
			{
				name:      "SMB server pod is excluded",
				request:   makeRequestVar(t, "Pod", storage.SMBServerPodName, "ns1"),
				wantMatch: false,
			},
			{
				name:      "non-Pod kind is excluded",
				request:   makeRequestVar(t, "ConfigMap", "some-cm", "ns1"),
				wantMatch: false,
			},
			{
				name:      "empty name matches (generateName pod)",
				request:   makeRequestVar(t, "Pod", "", "ns1"),
				wantMatch: true,
			},
			{
				name:      "name that extends the SMB name still matches",
				request:   makeRequestVar(t, "Pod", storage.SMBServerPodName+"-extra", "ns1"),
				wantMatch: true,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				vars := map[string]any{
					"request": tt.request,
					"object":  makeObjectVar(nil),
				}
				got := evalCEL(t, env, expr, vars)
				assert.Equal(t, tt.wantMatch, got)
			})
		}
	})

	t.Run("exclude-cred-init-job-pods", func(t *testing.T) {
		expr := matchConds[1].Expression

		tests := []struct {
			name      string
			request   map[string]any
			ownerRefs []map[string]any
			wantMatch bool
		}{
			{
				name:      "pod with no owner refs matches",
				request:   makeRequestVar(t, "Pod", "worker-xyz", "sr-foo"),
				ownerRefs: nil,
				wantMatch: true,
			},
			{
				name:      "pod owned by cred-init job is excluded",
				request:   makeRequestVar(t, "Pod", "sr-foo-cred-init-abc", "sr-foo"),
				ownerRefs: []map[string]any{ownerRef("Job", "sr-foo-cred-init")},
				wantMatch: false,
			},
			{
				name:      "pod owned by a different job matches",
				request:   makeRequestVar(t, "Pod", "some-pod", "sr-foo"),
				ownerRefs: []map[string]any{ownerRef("Job", "other-job")},
				wantMatch: true,
			},
			{
				name:      "cred-init from another namespace is not excluded",
				request:   makeRequestVar(t, "Pod", "some-pod", "sr-bar"),
				ownerRefs: []map[string]any{ownerRef("Job", "sr-foo-cred-init")},
				wantMatch: true,
			},
			{
				name:      "non-Job owner with cred-init name matches",
				request:   makeRequestVar(t, "Pod", "some-pod", "sr-foo"),
				ownerRefs: []map[string]any{ownerRef("ReplicaSet", "sr-foo-cred-init")},
				wantMatch: true,
			},
			{
				name:    "multiple owners, one is matching cred-init job, excluded",
				request: makeRequestVar(t, "Pod", "some-pod", "ns1"),
				ownerRefs: []map[string]any{
					ownerRef("ReplicaSet", "something"),
					ownerRef("Job", "ns1-cred-init"),
				},
				wantMatch: false,
			},
			{
				name:      "non-Pod kind is excluded",
				request:   makeRequestVar(t, "Service", "svc", "sr-foo"),
				ownerRefs: nil,
				wantMatch: false,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				vars := map[string]any{
					"request": tt.request,
					"object":  makeObjectVar(tt.ownerRefs),
				}
				got := evalCEL(t, env, expr, vars)
				assert.Equal(t, tt.wantMatch, got)
			})
		}
	})

	t.Run("both webhooks share identical match conditions", func(t *testing.T) {
		for i := range whs[0].MatchConditions {
			assert.Equal(t, whs[0].MatchConditions[i], whs[1].MatchConditions[i])
		}
	})
}
