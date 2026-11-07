// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/fnstatus"
)

const fsNamespace = "nvsnap-system"

func fsRecord(name string, labels, data map[string]string) *corev1.ConfigMap {
	l := map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}
	for k, v := range labels {
		l[k] = v
	}
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: fsNamespace, Labels: l}, Data: data}
}

var fsVersion = map[string]string{fnstatus.LabelFunctionVersion: "v-1", fnstatus.LabelFunction: "f-1",
	fnstatus.LabelTenant: "t-1", fnstatus.LabelNamespace: "sr-1"}

func functionStatus(objs ...runtime.Object) (*FunctionStatus, *kubefake.Clientset, *dynfake.FakeDynamicClient) {
	kc := kubefake.NewClientset(objs...)
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{functionGVR: "NvsnapFunctionList"})
	return &FunctionStatus{KubeClient: kc, DynClient: dc, Namespace: fsNamespace}, kc, dc
}

func getFunction(t *testing.T, dc *dynfake.FakeDynamicClient, name string) *unstructured.Unstructured {
	t.Helper()
	u, err := dc.Resource(functionGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("NvsnapFunction %s: %v", name, err)
	}
	return u
}

func writes(dc *dynfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dc.Actions() {
		switch a.GetVerb() {
		case "create", "update", "delete":
			n++
		}
	}
	return n
}

func TestFunctionStatus_Lifecycle(t *testing.T) {
	ctx := context.Background()
	f, kc, dc := functionStatus(fsRecord("nvsnap-criu-group-a", fsVersion, map[string]string{"state": "capturing", "size": "2"}))

	// A version gets its object with its first record.
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	u := getFunction(t, dc, "v-1")
	if st, _, _ := unstructured.NestedString(u.Object, "status", "state"); st != "capturing" {
		t.Errorf("state = %q", st)
	}
	if u.GetKind() != "NvsnapFunction" || u.GetLabels()[fnstatus.LabelFunction] != "f-1" ||
		u.GetLabels()["app.kubernetes.io/managed-by"] != functionManagedBy {
		t.Errorf("object = %v %v", u.GetKind(), u.GetLabels())
	}

	// Unchanged records write nothing.
	dc.ClearActions()
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := writes(dc); n != 0 {
		t.Errorf("an unchanged pass wrote %d times", n)
	}

	// A record's change is projected.
	cm, _ := kc.CoreV1().ConfigMaps(fsNamespace).Get(ctx, "nvsnap-criu-group-a", metav1.GetOptions{})
	cm.Data = map[string]string{"state": "complete", "size": "2", "checkpoint.0": "c0", "checkpoint.1": "c1",
		"restores": "2026-11-03T11:00:00Z=42.5"}
	if _, err := kc.CoreV1().ConfigMaps(fsNamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	u = getFunction(t, dc, "v-1")
	st, _, _ := unstructured.NestedString(u.Object, "status", "state")
	method, _, _ := unstructured.NestedString(u.Object, "status", "method")
	secs, _, _ := unstructured.NestedFloat64(u.Object, "status", "lastRestoreSeconds")
	if st != "complete" || method != "criu" || secs != 42.5 {
		t.Errorf("status = %q %q %v", st, method, secs)
	}

	// The object goes with the version's last record.
	if err := kc.CoreV1().ConfigMaps(fsNamespace).Delete(ctx, "nvsnap-criu-group-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Resource(functionGVR).Get(ctx, "v-1", metav1.GetOptions{}); err == nil {
		t.Error("the object outlived its records")
	}
}

// TestFunctionStatus_KeepsOthers: objects this server did not make are
// never deleted or overwritten.
func TestFunctionStatus_KeepsOthers(t *testing.T) {
	ctx := context.Background()
	f, _, dc := functionStatus(fsRecord("nvsnap-criu-group-a", fsVersion, map[string]string{"state": "failed", "size": "1"}))
	other := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "nvsnap.io/v1alpha1", "kind": functionKind}}
	other.SetName("someone-elses")
	if _, err := dc.Resource(functionGVR).Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	getFunction(t, dc, "someone-elses")
	getFunction(t, dc, "v-1")
}

// TestFunctionStatus_ListFailureDeletesNothing: a failed read of the
// records never reads as "nothing is cached".
func TestFunctionStatus_ListFailureDeletesNothing(t *testing.T) {
	ctx := context.Background()
	f, kc, dc := functionStatus(fsRecord("nvsnap-criu-group-a", fsVersion, map[string]string{"state": "failed", "size": "1"}))
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	kc.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	if err := f.reconcileOnce(ctx); err == nil {
		t.Fatal("a failed list was not reported")
	}
	getFunction(t, dc, "v-1")
}

// TestFunctionStatus_OnlyItsNamespace: records are read from the records'
// namespace and only nvsnap's.
func TestFunctionStatus_OnlyItsNamespace(t *testing.T) {
	ctx := context.Background()
	elsewhere := fsRecord("nvsnap-criu-group-b", fsVersion, map[string]string{"state": "complete"})
	elsewhere.Namespace = "default"
	unmanaged := fsRecord("nvsnap-criu-group-c", fsVersion, map[string]string{"state": "complete"})
	unmanaged.Labels = map[string]string{fnstatus.LabelFunctionVersion: "v-1"}
	f, _, dc := functionStatus(elsewhere, unmanaged)
	if err := f.reconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if l, _ := dc.Resource(functionGVR).List(ctx, metav1.ListOptions{}); len(l.Items) != 0 {
		t.Errorf("objects from records outside the namespace or not nvsnap's: %d", len(l.Items))
	}
}

// TestSameJSON: a status the API server returns holds int64 where the one
// written held float64; they must compare equal, or every pass rewrites
// every object.
func TestSameJSON(t *testing.T) {
	read := map[string]any{"ranks": int64(2), "lastRestoreSeconds": 42.5, "configurations": []any{map[string]any{"ranks": int64(2)}}}
	written := map[string]any{"ranks": float64(2), "lastRestoreSeconds": 42.5, "configurations": []any{map[string]any{"ranks": float64(2)}}}
	if !sameJSON(read, written) {
		t.Error("int64 and float64 of one status differ")
	}
	written["ranks"] = float64(3)
	if sameJSON(read, written) {
		t.Error("different statuses compare equal")
	}
}
