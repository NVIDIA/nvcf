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

package selfhosted

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// -- probeStaleNamespaces --

func TestProbeStaleNamespaces_AbsentIsHealthy(t *testing.T) {
	// A namespace that doesn't exist is not stale - it has simply never been
	// created or was already fully deleted.
	client := fake.NewSimpleClientset()
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf", "sis"})
	require.NoError(t, err)
	assert.Empty(t, stale, "absent namespaces must not be reported as stale")
}

func TestProbeStaleNamespaces_TerminatingIsByDeletionTimestamp(t *testing.T) {
	now := metav1.Now()
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &now},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Terminating")
}

func TestProbeStaleNamespaces_TerminatingIsByPhase(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "sis"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"sis"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "sis", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Terminating")
}

func TestProbeStaleNamespaces_EmptyShellNoHelmSecrets(t *testing.T) {
	// Namespace exists and is Active but holds no Helm release secrets →
	// leftover empty shell from a partial helm uninstall.
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Helm release")
}

func TestProbeStaleNamespaces_HealthyReleaseNotStale(t *testing.T) {
	// Namespace exists and carries an owner=helm secret → active Helm release,
	// not stale.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sh.helm.release.v1.nvcf.v1",
				Namespace: "nvcf",
				Labels:    map[string]string{"owner": "helm", "name": "nvcf", "status": "deployed"},
			},
		},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, stale, "namespace with an active Helm release must not be stale")
}

func TestProbeStaleNamespaces_HealthyReleaseConfigMapDriverNotStale(t *testing.T) {
	// HELM_DRIVER=configmap stores release objects as ConfigMaps with owner=helm.
	// A namespace with an owner=helm ConfigMap and no Helm Secret must not be
	// reported as stale.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nvcf.v1",
				Namespace: "nvcf",
				Labels:    map[string]string{"owner": "helm", "name": "nvcf", "status": "deployed"},
			},
		},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, stale, "namespace with an owner=helm ConfigMap (configmap driver) must not be stale")
}

func TestProbeStaleNamespaces_MixedNamespaces(t *testing.T) {
	// One absent, one healthy, one terminating, one empty shell.
	now := metav1.Now()
	client := fake.NewSimpleClientset(
		// "sis" - healthy with a Helm release
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sis"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sh.helm.release.v1.sis.v1", Namespace: "sis",
				Labels: map[string]string{"owner": "helm"},
			},
		},
		// "nvcf" - stuck terminating
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &now},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
		// "api-keys" - empty shell
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "api-keys"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		// "cassandra-system" - absent (not present in fake)
	)

	namespaces := []string{"cassandra-system", "sis", "nvcf", "api-keys"}
	stale, err := probeStaleNamespaces(context.Background(), client, namespaces)
	require.NoError(t, err)
	require.Len(t, stale, 2, "only nvcf (terminating) and api-keys (empty shell) should be stale")

	staleNames := make(map[string]string, 2)
	for _, s := range stale {
		staleNames[s.Name] = s.Reason
	}
	assert.Contains(t, staleNames, "nvcf")
	assert.Contains(t, staleNames["nvcf"], "Terminating")
	assert.Contains(t, staleNames, "api-keys")
	assert.Contains(t, staleNames["api-keys"], "Helm release")
}

// -- staleNamespaceCheck binaryCheckSpec --

// pinCurrentKubeContext keeps hint assertions independent of the developer's
// kubeconfig: without it, a machine with a current-context makes every hint
// carry a --context flag the test did not ask for.
func pinCurrentKubeContext(t *testing.T, name string) {
	t.Helper()
	prev := currentKubeContextNameFn
	currentKubeContextNameFn = func() string { return name }
	t.Cleanup(func() { currentKubeContextNameFn = prev })
}

func TestStaleNamespaceCheck_ProberErrorDegradestoWarning(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// A prober that cannot contact the cluster must not fail the overall check
	// at error severity - it would produce false failures on transient network
	// issues or misconfigured kubeconfigs.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, fmt.Errorf("cluster unreachable")
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "warning", r.Severity,
		"prober errors must degrade to warning so transient failures do not block the operator")
	assert.Contains(t, r.Message, "cluster unreachable")
}

func TestStaleNamespaceCheck_StaleIsError(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// A successfully detected stale namespace must fail at error severity so
	// anyFailed trips the non-zero exit code.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "error", r.Severity,
		"detected stale namespaces must use error severity so the exit code is non-zero")
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "/api/v1/namespaces/nvcf/finalize",
		"the remediation must use the finalize subresource; a plain patch is silently reverted")
}

func TestStaleNamespaceCheck_CleanPasses(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())
	assert.True(t, r.Passed)
}

func TestStaleNamespaceCheck_MessageNamesAllStaleNamespaces(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// The remediation command must name every stale namespace so the operator
	// can copy-paste it without having to cross-reference the check output.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "api-keys", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "api-keys"}).Run(context.Background())
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "api-keys")
	assert.Contains(t, r.Message, "/api/v1/namespaces/nvcf/finalize",
		"the remediation must use the finalize subresource; a plain patch is silently reverted")
	assert.Contains(t, r.Message, "kubectl delete namespace api-keys")
}

// A namespace on a real install holds many non-Helm Secrets (ServiceAccount
// tokens, TLS, pull secrets) that sort before sh.helm.release.v1.*. The
// apiserver applies the label selector after paging, so the first page can
// legitimately come back empty with a Continue token. Treating that as "no
// release" reports a live namespace stale and tells the operator to delete it.
func TestProbeStaleNamespaces_PagesPastNonMatchingObjects(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})

	// First page: empty with a Continue token. Second page: the release secret.
	call := 0
	client.PrependReactor("list", "secrets", func(_ ktesting.Action) (bool, runtime.Object, error) {
		call++
		if call == 1 {
			return true, &corev1.SecretList{
				ListMeta: metav1.ListMeta{Continue: "next-page-token"},
			}, nil
		}
		return true, &corev1.SecretList{Items: []corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sh.helm.release.v1.nvcf.v1", Namespace: "nvcf",
				Labels: map[string]string{"owner": "helm"},
			},
		}}}, nil
	})

	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, stale,
		"an empty first page with a Continue token must not be read as 'no Helm release'")
	assert.Greater(t, call, 1, "the probe must follow the Continue token")
}

// The remediation for a hit is "delete this namespace", so the list must only
// contain namespaces a helmfile release actually owns. nvcf-backend is created
// at runtime by NVCA and holds live worker pods.
func TestControlPlaneNamespaceList_ExcludesRuntimeOwnedNamespaces(t *testing.T) {
	for _, ns := range []string{"nvcf-backend", "openbao-system"} {
		assert.NotContains(t, nvcfControlPlaneNamespaces, ns,
			"%s hosts no Helm release; probing it yields a destructive false positive", ns)
	}
	for _, ns := range []string{"cert-manager", "nvcf-ui"} {
		assert.Contains(t, nvcfControlPlaneNamespaces, ns,
			"%s is a real stack namespace and must be probed", ns)
	}
	assert.NotContains(t, nvcfComputePlaneNamespaces, "nvca-system",
		"nvca-system is operator-created and hosts no release")
}

// TestStaleNamespaceCheck_HintsPinTheProbedContext guards the remediation
// hints in split mode. The two callers probe different clusters, so a bare
// kubectl command in a hint runs against whatever context is current, and
// these hints delete namespaces.
func TestStaleNamespaceCheck_HintsPinTheProbedContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "api-keys", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "cp-ctx", []string{"nvcf", "api-keys"}).Run(context.Background())

	// Every kubectl invocation, including the one piped into xargs, has to
	// carry the flag: one unpinned command is enough to hit the wrong cluster.
	for _, cmd := range strings.Split(r.Message, "kubectl ")[1:] {
		assert.True(t, strings.HasPrefix(cmd, "--context cp-ctx "),
			"unpinned kubectl invocation in hint: kubectl %s", cmd)
	}
	assert.Contains(t, r.Message, "kubectl --context cp-ctx delete namespace api-keys")
}

func TestStaleNamespaceCheck_HintsQuoteTheContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "my ctx", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "--context 'my ctx' delete namespace nvcf",
		"a context name with a space must be quoted so the pasted command does not split it")
}

// TestStaleNamespaceCheck_HintsSubstituteTheNamespace guards against a bare
// `<ns>` placeholder in a pasteable command: the shell reads `<` and `>` as
// redirection, so the command fails before kubectl runs.
func TestStaleNamespaceCheck_HintsSubstituteTheNamespace(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "sis", Reason: "stuck Terminating"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())

	assert.NotContains(t, r.Message, "<ns>",
		"a shell-redirection placeholder must not appear in a copy-pasteable command")
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n nvcf ")
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n sis ")
}

// TestStaleNamespaceCheck_HintsNameTheCurrentContext covers the single-cluster
// path. The probe followed the current-context, so the hint has to name it:
// the operator can switch context between reading this and pasting it, and
// these commands delete namespaces.
func TestStaleNamespaceCheck_HintsNameTheCurrentContext(t *testing.T) {
	pinCurrentKubeContext(t, "k3d-local")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "kubectl --context k3d-local delete namespace nvcf")
}

// An unreadable kubeconfig must not break the hint; it degrades to no flag.
func TestStaleNamespaceCheck_HintsOmitUnknownContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "kubectl delete namespace nvcf")
}
