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

package nvca

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	k8smock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil/mock"
	fakebartclient "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// TestAgentCachesStripManagedFields_Envtest builds the agent's caches against a
// real API server with the agent's own code, then checks that no cached object
// carries managedFields, that live reads still return them, and that an Update
// built from a cached copy keeps the server's entries.
//
// Uses a plain envtest.Environment without NVCA CRDs: the ICMSRequest and
// StorageRequest informers run against a fake clientset.
func TestAgentCachesStripManagedFields_Envtest(t *testing.T) {
	binAssetsDir := os.Getenv("KUBEBUILDER_ASSETS")
	if binAssetsDir == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: binAssetsDir}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	live, err := client.New(cfg, client.Options{Scheme: mgrScheme})
	require.NoError(t, err)

	const testNamespace = "managed-fields-test"
	for _, name := range []string{SystemNamespace, RequestsNamespace, testNamespace} {
		require.NoError(t, live.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	}
	for _, cm := range []*corev1.ConfigMap{
		bartBackendGPUsConfigMap(),
		k8smock.NewNetworkPolicyConfigMap(SystemNamespace),
		newHelmInstanceRBACConfigMap(),
	} {
		require.NoError(t, live.Create(ctx, cm))
	}

	// Every fixture ends up with managedFields entries from two field managers.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: RequestsNamespace, Name: "pod-1"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "registry.example/main:1.0"}},
		},
	}
	require.NoError(t, live.Create(ctx, pod, client.FieldOwner("test-creator")))
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	require.NoError(t, live.Status().Update(ctx, pod, client.FieldOwner("test-status-writer")))

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "node-1",
		Labels: map[string]string{"nvidia.com/gpu.present": "true"},
	}}
	instanceNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "instance-1",
		Labels: map[string]string{nvcatypes.WorkloadInstanceTypeLabel: nvcatypes.WorkloadInstanceTypeValueMiniService},
	}}
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: RequestsNamespace, Name: "pod-1.backoff"},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "v1", Kind: "Pod", Namespace: RequestsNamespace, Name: pod.Name,
		},
		Type:    corev1.EventTypeWarning,
		Reason:  "BackOff",
		Message: "Back-off restarting failed container",
	}
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "lease-1"},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       ptr.To("holder-1"),
			LeaseDurationSeconds: ptr.To[int32](60),
			AcquireTime:          &metav1.MicroTime{Time: time.Now()},
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "config-1"},
		Data:       map[string]string{"key": "value"},
	}
	for _, obj := range []client.Object{node, instanceNamespace, event, lease, configMap} {
		require.NoError(t, live.Create(ctx, obj, client.FieldOwner("test-creator")))
		obj.SetAnnotations(map[string]string{"test": "updated"})
		require.NoError(t, live.Update(ctx, obj, client.FieldOwner("test-annotator")))
	}
	for _, obj := range []client.Object{pod, node, instanceNamespace, event, lease, configMap} {
		require.Len(t, obj.GetManagedFields(), 2, "%T %s", obj, obj.GetName())
	}

	t.Run("controller manager cache", func(t *testing.T) {
		mgr, err := ctrl.NewManager(cfg, manager.Options{
			Scheme:                  mgrScheme,
			GracefulShutdownTimeout: new(time.Duration),
			WebhookServer:           fakeWebhookServer{},
			Metrics:                 nvcaenvtest.NewFakeMetricsOptions(),
			Cache:                   agentManagerCacheOptions(),
		})
		require.NoError(t, err)
		// Registered before the manager starts, like the controllers' Pod watches.
		_, err = mgr.GetCache().GetInformer(ctx, &corev1.Pod{})
		require.NoError(t, err)

		mgrCtx, mgrCancel := context.WithCancel(ctx)
		mgrErrCh, err := nvcaenvtest.StartManager(mgrCtx, mgr)
		require.NoError(t, err)
		t.Cleanup(func() {
			mgrCancel()
			<-mgrErrCh
		})

		// Events go through their ByObject cache. The Lease and ConfigMap
		// informers are created by the first cached Get.
		cached := mgr.GetClient()
		for _, want := range []client.Object{pod, event, lease, configMap} {
			got := want.DeepCopyObject().(client.Object)
			require.NoError(t, cached.Get(ctx, client.ObjectKeyFromObject(want), got))
			assert.Nil(t, got.GetManagedFields(), "cached %T", got)
		}
		pods := &corev1.PodList{}
		require.NoError(t, cached.List(ctx, pods, client.InNamespace(RequestsNamespace)))
		require.Len(t, pods.Items, 1)
		assert.Nil(t, pods.Items[0].ManagedFields)

		for _, want := range []client.Object{pod, event, lease, configMap} {
			got := want.DeepCopyObject().(client.Object)
			require.NoError(t, mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(want), got))
			assert.Len(t, got.GetManagedFields(), 2, "live %T", got)
		}

		// The model cache renews and takes over leases with an Update built
		// from a cached Get.
		cachedLease := &coordv1.Lease{}
		require.NoError(t, cached.Get(ctx, client.ObjectKeyFromObject(lease), cachedLease))
		cachedLease.Spec.HolderIdentity = ptr.To("holder-2")
		cachedLease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
		require.NoError(t, cached.Update(ctx, cachedLease))

		liveLease := &coordv1.Lease{}
		require.NoError(t, mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(lease), liveLease))
		assert.Equal(t, "holder-2", ptr.Deref(liveLease.Spec.HolderIdentity, ""))
		managers := make([]string, 0, len(liveLease.ManagedFields))
		for _, entry := range liveLease.ManagedFields {
			managers = append(managers, entry.Manager)
		}
		assert.Contains(t, managers, "test-creator")
		assert.Contains(t, managers, "test-annotator")
		assert.Len(t, managers, 3, "the update adds an entry for its own field manager")

		assert.EventuallyWithT(t, func(ct *assert.CollectT) {
			got := &coordv1.Lease{}
			if assert.NoError(ct, cached.Get(ctx, client.ObjectKeyFromObject(lease), got)) {
				assert.Equal(ct, "holder-2", ptr.Deref(got.Spec.HolderIdentity, ""))
				assert.Nil(ct, got.ManagedFields)
			}
		}, 10*time.Second, 50*time.Millisecond)
	})

	t.Run("client-go caches", func(t *testing.T) {
		clientset, err := kubernetes.NewForConfig(cfg)
		require.NoError(t, err)
		clients := &kubeclients.KubeClients{
			Config:          cfg,
			K8s:             clientset,
			BART:            fakebartclient.NewSimpleClientset(),
			HelmV2:          ctrlfake.NewClientBuilder().WithScheme(newMiniServiceScheme()).Build(),
			DiscoveryClient: clientset.Discovery(),
		}
		bcCtx, bcCancel := context.WithCancel(ctx)
		t.Cleanup(bcCancel)
		bc, _, err := NewBackendk8sCacheBuilder().
			WithNamespaceLabels(labels.Set{"foo": "bar"}).
			WithClients(clients).
			Start(bcCtx)
		require.NoError(t, err)

		cachedPod, err := bc.podLister.Pods(pod.Namespace).Get(pod.Name)
		require.NoError(t, err)
		assert.Nil(t, cachedPod.ManagedFields)
		cachedNode, err := bc.nodeLister.Get(node.Name)
		require.NoError(t, err)
		assert.Nil(t, cachedNode.ManagedFields)
		cachedNamespace, err := bc.instanceNamespaceLister.Get(instanceNamespace.Name)
		require.NoError(t, err)
		assert.Nil(t, cachedNamespace.ManagedFields)

		livePod, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Len(t, livePod.ManagedFields, 2)
	})
}

// TestAgentCacheConstructorsStripManagedFields parses this package's sources
// and checks how the agent builds its caches: every informer factory passes
// WithTransform(k8sutil.StripManagedFields), the controller manager takes
// agentManagerCacheOptions(), and the nvsnap controller is given
// k8sutil.StripManagedFields. The other tests show that these options strip
// managedFields. This check also covers the informers that no test starts,
// such as the ones behind feature flags.
func TestAgentCacheConstructorsStripManagedFields(t *testing.T) {
	funcName := func(call *ast.CallExpr) string {
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			return fn.Sel.Name
		}
		return ""
	}
	isStripManagedFields := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StripManagedFields" {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "k8sutil"
	}
	isStripTransformOption := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		return ok && funcName(call) == "WithTransform" && len(call.Args) == 1 && isStripManagedFields(call.Args[0])
	}
	isAgentCacheOptions := func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		key, isIdent := kv.Key.(*ast.Ident)
		value, isCall := kv.Value.(*ast.CallExpr)
		return isIdent && key.Name == "Cache" && isCall && funcName(value) == "agentManagerCacheOptions"
	}
	isStripTransformField := func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return false
		}
		field, ok := assign.Lhs[0].(*ast.SelectorExpr)
		return ok && field.Sel.Name == "Transform" && isStripManagedFields(assign.Rhs[0])
	}
	contains := func(root ast.Node, match func(ast.Node) bool) bool {
		found := false
		ast.Inspect(root, func(n ast.Node) bool {
			found = found || (n != nil && match(n))
			return !found
		})
		return found
	}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var factories, managers, nvsnapStarts int
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				pos := fset.Position(n.Pos())
				switch funcName(n) {
				case "NewSharedInformerFactory", "NewFilteredSharedInformerFactory":
					t.Errorf("%s: this informer factory constructor takes no WithTransform option", pos)
				case "NewSharedInformerFactoryWithOptions":
					factories++
					assert.True(t, contains(n, isStripTransformOption),
						"%s: informer factory without WithTransform(k8sutil.StripManagedFields)", pos)
				case "NewManager":
					managers++
					assert.True(t, contains(n, isAgentCacheOptions),
						"%s: controller manager without Cache: agentManagerCacheOptions()", pos)
				}
			case *ast.FuncDecl:
				if n.Name.Name == "startNvSnapController" {
					nvsnapStarts++
					assert.True(t, contains(n, isStripTransformField),
						"%s: nvsnap controller without Transform = k8sutil.StripManagedFields", fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
	// Under Bazel the sources come from the test's data files.
	assert.NotZero(t, factories, "no informer factory found in %v", files)
	assert.Equal(t, 1, managers, "controller managers found")
	assert.Equal(t, 1, nvsnapStarts, "startNvSnapController functions found")
}
