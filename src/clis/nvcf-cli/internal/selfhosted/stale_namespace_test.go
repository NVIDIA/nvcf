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

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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

func TestProbeStaleNamespaces_NoReleaseWithInstallDataIsStale(t *testing.T) {
	// Namespace exists and is Active, holds no Helm release, and still holds a
	// volume claim -> leftover of a partial helm uninstall. The second
	// namespace carries a release and is not reported.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "nvcf"}},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sis"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.sis.v1",
			Namespace: "sis",
			Labels:    map[string]string{"owner": "helm", "name": "sis", "status": "deployed"},
		}},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf", "sis"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Helm release")
}

func TestProbeStaleNamespaces_HealthyReleaseNotStale(t *testing.T) {
	// Namespace exists and carries an owner=helm secret -> active Helm release,
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
	// One absent, one healthy, one terminating, one left with its data, and
	// one empty, as before a first install.
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
		// "api-keys" - no release, its volume claim left behind
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "api-keys"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "api-keys"}},
		// "ess" - empty, created ahead of a first install
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "ess"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		// "cassandra-system" - absent (not present in fake)
	)

	namespaces := []string{"cassandra-system", "sis", "nvcf", "api-keys", "ess"}
	stale, err := probeStaleNamespaces(context.Background(), client, namespaces)
	require.NoError(t, err)
	require.Len(t, stale, 2, "only nvcf (terminating) and api-keys (data left behind) should be stale")

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
	assert.Equal(t, SeverityWarning, r.Severity,
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
	assert.Equal(t, SeverityError, r.Severity,
		"detected stale namespaces must use error severity so the exit code is non-zero")
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "/api/v1/namespaces/nvcf/finalize",
		"the remediation must use the finalize subresource; a plain patch is silently reverted")
	inspect := strings.Index(r.Message, "xargs -n1 kubectl get -n nvcf")
	force := strings.Index(r.Message, "/finalize")
	require.GreaterOrEqual(t, inspect, 0)
	assert.Less(t, inspect, force, "inspecting the namespace comes before force-clearing its finalizers")
}

func TestStaleNamespaceCheck_CleanPasses(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())
	assert.True(t, r.Passed)
	assert.Equal(t, SeverityInfo, r.Severity, "a clean pass must not carry the error severity it starts with")
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
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n api-keys --show-kind --ignore-not-found")
	assert.NotContains(t, r.Message, "get all", "get all omits PVCs, Secrets and custom resources")
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
	assert.Contains(t, r.Message, "xargs -n1 kubectl --context cp-ctx get -n api-keys")
}

func TestStaleNamespaceCheck_HintsQuoteTheContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "my ctx", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "--context 'my ctx' get -n nvcf",
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
	assert.Contains(t, r.Message, "xargs -n1 kubectl --context k3d-local get -n nvcf")
}

// An unreadable kubeconfig must not break the hint; it degrades to no flag.
func TestStaleNamespaceCheck_HintsOmitUnknownContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n nvcf")
}

// The probe must run against the same context the hints name. Resolving the
// current-context after probing leaves a window where it changes in between,
// which would have the delete hints target a cluster that was never read.
func TestStaleNamespaceCheck_ProbesTheResolvedContext(t *testing.T) {
	pinCurrentKubeContext(t, "k3d-local")
	var probed string
	prober := func(_ context.Context, kctx string, _ []string) ([]StaleNamespace, error) {
		probed = kctx
		return nil, nil
	}
	staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Equal(t, "k3d-local", probed,
		"the probe must target the resolved context, not whatever is current when it runs")
}

// `down` destroys every release but keeps the namespaces and their PVCs, so no
// owner=helm object exists anywhere. With the default driver that is exactly
// the stale state to report; a reinstall would silently reattach the old
// Cassandra and OpenBao volumes. Helm never deletes hook Jobs, so their
// Completed pods are still there, and they are not a live install.
func TestProbeStaleNamespaces_AfterDownReportsEveryNamespace(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	var objects []runtime.Object
	for _, ns := range []string{"cassandra-system", "vault-system"} {
		objects = append(objects,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-0", Namespace: ns}},
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "initialize-cluster", Namespace: ns}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "initialize-cluster-x1", Namespace: ns,
					OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: "initialize-cluster"}}},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
		)
	}
	client := fake.NewSimpleClientset(objects...)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"cassandra-system", "vault-system"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// Only a Pending or Running pod that is neither terminating nor a Job's is a
// live workload. A hook Job's pod, even one still running, and a pod being
// deleted do not keep a namespace holding install data from being reported.
func TestProbeStaleNamespaces_OnlyALiveWorkloadIsAnInstall(t *testing.T) {
	now := metav1.Now()
	pod := func(ns string, phase corev1.PodPhase, mutate func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns}, Status: corev1.PodStatus{Phase: phase}}
		if mutate != nil {
			mutate(p)
		}
		return p
	}
	cases := map[string]struct {
		pod   *corev1.Pod
		stale bool
	}{
		"running":     {pod("a", corev1.PodRunning, nil), false},
		"pending":     {pod("b", corev1.PodPending, nil), false},
		"succeeded":   {pod("c", corev1.PodSucceeded, nil), true},
		"terminating": {pod("d", corev1.PodRunning, func(p *corev1.Pod) { p.DeletionTimestamp = &now }), true},
		"hook job's": {pod("e", corev1.PodRunning, func(p *corev1.Pod) {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: "migrations"}}
		}), true},
	}
	for name, tc := range cases {
		ns := tc.pod.Namespace
		client := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: ns}},
			tc.pod,
		)
		got, err := probeStaleNamespaces(context.Background(), client, []string{ns})
		require.NoError(t, err, name)
		assert.Equal(t, tc.stale, len(got) == 1, name)
	}
}

// HELM_DRIVER=sql keeps release state in a database, so the absence of an
// in-cluster release object says nothing and must not be reported.
func TestProbeStaleNamespaces_SQLDriverSkipsTheNoReleaseSignal(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
	)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A namespace stuck Terminating is still reported even when no Helm release
// object exists anywhere: that signal does not depend on the storage driver.
func TestProbeStaleNamespaces_TerminatingReportedWithoutHelmObjects(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
	)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "stuck Terminating", got[0].Reason)
}

// A conditionally-disabled component installed the upstream way is healthy.
// Reporting it at error severity turns a supported shape into exit 2, and the
// old remediation would have destroyed every Certificate in the cluster.
func TestStaleNamespaceCheck_NoHelmReleaseWarnsAndDoesNotSuggestDelete(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "cert-manager", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"cert-manager"}).Run(context.Background())

	assert.Equal(t, SeverityWarning, r.Severity,
		"a namespace with no Helm release must not fail the run")
	assert.NotContains(t, r.Message, "delete namespace",
		"the remediation must not destroy a namespace the operator may own")
}

// A namespace stuck Terminating does block the run.
func TestStaleNamespaceCheck_TerminatingIsStillAnError(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Equal(t, SeverityError, r.Severity)
}

// kubectl keeps only the last -n it is given, so a joined command inspects one
// namespace and reports the rest as empty, which is the opposite of what an
// operator deciding whether to delete them needs.
func TestStaleNamespaceCheck_HintsOneCommandPerNamespace(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "api-keys", Reason: "no Helm release"},
			{Name: "ess", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"api-keys", "ess"}).Run(context.Background())

	assert.NotContains(t, r.Message, "-n api-keys -n ess",
		"a joined namespace list silently inspects only the last one")
	assert.Contains(t, r.Message, "get -n api-keys --show-kind")
	assert.Contains(t, r.Message, "get -n ess --show-kind")
}

// The documented install pre-creates namespaces before the first install, so
// none has a release yet: labelled and empty, or holding a pull Secret, a TLS
// Secret, or the CA ConfigMap a mesh or the platform adds. None of that is a
// leftover. A namespace that still holds volume claims or workload objects, as
// `down` leaves them, is.
func TestProbeStaleNamespaces_PreCreatedNamespaceIsNotStale(t *testing.T) {
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"nvcf/platform": "true"}},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}
	}
	secret := func(ns, name string, typ corev1.SecretType) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Type: typ}
	}
	configMap := func(ns, name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}
	client := fake.NewSimpleClientset(
		ns("api-keys"),
		ns("ess"), secret("ess", "nvcr-pull-secret", corev1.SecretTypeDockerConfigJson),
		ns("nvcf"), secret("nvcf", "stargate-quic-tls", corev1.SecretTypeTLS), configMap("nvcf", "kube-root-ca.crt"),
		ns("sis"), configMap("sis", "istio-ca-root-cert"), configMap("sis", "openshift-service-ca.crt"),
		ns("cassandra-system"), secret("cassandra-system", "nvcr-pull-secret", corev1.SecretTypeDockerConfigJson),
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-cassandra-0", Namespace: "cassandra-system"}},
		ns("vault-system"), secret("vault-system", "vault-unseal", corev1.SecretTypeOpaque),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "openbao-server", Namespace: "vault-system"}},
	)
	stale, err := probeStaleNamespaces(context.Background(), client,
		[]string{"api-keys", "ess", "nvcf", "sis", "cassandra-system", "vault-system"})
	require.NoError(t, err)
	var names []string
	for _, s := range stale {
		names = append(names, s.Name)
	}
	assert.ElementsMatch(t, []string{"cassandra-system", "vault-system"}, names,
		"only the namespaces that still hold volume claims or workload objects")
}

// An install rendered with `helm template` (Argo CD) records no Helm release
// but runs pods. That is a live install, not a leftover, though it holds what
// a leftover would.
func TestProbeStaleNamespaces_NamespaceRunningPodsIsNotStale(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nats-system"},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "nats", Namespace: "nats-system"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nats-js-nats-0", Namespace: "nats-system"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nats-0", Namespace: "nats-system"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "nats-auth", Namespace: "nats-system"},
			Type: corev1.SecretTypeOpaque},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nats-system"})
	require.NoError(t, err)
	assert.Empty(t, stale)
}
