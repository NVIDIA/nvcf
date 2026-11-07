// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/fnstatus"
)

// functionGVR is NvsnapFunction: one object per function version, what
// nvsnap holds for it (docs/design/FUNCTION-CACHE-STATUS.md). Read-only for
// everyone but this server.
var functionGVR = schema.GroupVersionResource{Group: "nvsnap.io", Version: "v1alpha1", Resource: "nvsnapfunctions"}

const (
	functionKind = "NvsnapFunction"
	// functionManagedBy marks the objects this server keeps: only those
	// are deleted when their records are gone.
	functionManagedBy = "nvsnap-server"
)

// FunctionStatus keeps one NvsnapFunction per function version the capture
// records name, from the records in Namespace. The records are the source
// of truth: an object is rewritten when its records change, created for a
// version that gets its first record, and deleted when its last record is
// gone. Nothing nvsnap decides reads the objects.
type FunctionStatus struct {
	KubeClient kubernetes.Interface
	DynClient  dynamic.Interface
	Namespace  string        // where the records live
	Interval   time.Duration // default 30s
	Log        logrus.FieldLogger
}

// Run keeps the objects every Interval until ctx is cancelled.
func (f *FunctionStatus) Run(ctx context.Context) {
	if f.Interval == 0 {
		f.Interval = 30 * time.Second
	}
	if f.Log == nil {
		f.Log = logrus.NewEntry(logrus.New()).WithField("subsys", "server.function-status")
	}
	missing := false
	t := time.NewTicker(f.Interval)
	defer t.Stop()
	for {
		err := f.reconcileOnce(ctx)
		switch {
		case apierrors.IsNotFound(err) && !missing:
			missing = true
			f.Log.Info("function status: the NvsnapFunction CRD is not installed; not kept")
		case err != nil && !apierrors.IsNotFound(err):
			f.Log.WithError(err).Warn("function status: cannot keep the NvsnapFunction objects")
		case err == nil:
			missing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcileOnce makes the objects match the records once.
func (f *FunctionStatus) reconcileOnce(ctx context.Context) error {
	records, err := f.KubeClient.CoreV1().ConfigMaps(f.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=nvsnap"})
	if err != nil {
		// Never delete on a list that failed: an empty view is a claim
		// that nothing is cached.
		return err
	}
	want := fnstatus.Project(records.Items)
	objs := f.DynClient.Resource(functionGVR)
	have, err := objs.List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=" + functionManagedBy})
	if err != nil {
		return err
	}
	existing := make(map[string]*unstructured.Unstructured, len(have.Items))
	for i := range have.Items {
		existing[have.Items[i].GetName()] = &have.Items[i]
	}
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for name := range want {
		s := want[name]
		status, err := toMap(s)
		if err != nil {
			keep(err)
			continue
		}
		obj, ok := existing[name]
		if !ok {
			_, err = objs.Create(ctx, functionObject(name, s, status), metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				err = nil // not ours, or made since the list; the next pass sees it
			}
			keep(err)
			continue
		}
		delete(existing, name)
		cur, _, _ := unstructured.NestedMap(obj.Object, "status")
		if sameJSON(cur, status) && maps.Equal(obj.GetLabels(), functionLabels(s)) {
			continue
		}
		next := functionObject(name, s, status)
		next.SetResourceVersion(obj.GetResourceVersion())
		_, err = objs.Update(ctx, next, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			err = nil // the next pass sees it
		}
		keep(err)
	}
	for name := range existing {
		err := objs.Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			err = nil
		}
		keep(err)
	}
	return firstErr
}

// functionLabels select a function's versions: kubectl get nvsnapfunctions
// -l nvsnap.io/function-id=<id>.
func functionLabels(s fnstatus.Status) map[string]string {
	l := map[string]string{"app.kubernetes.io/managed-by": functionManagedBy}
	for k, v := range map[string]string{fnstatus.LabelFunction: s.FunctionID, fnstatus.LabelTenant: s.Tenant, fnstatus.LabelNamespace: s.Namespace} {
		if v != "" {
			l[k] = v
		}
	}
	return l
}

func functionObject(name string, s fnstatus.Status, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": functionGVR.GroupVersion().String(),
		"kind":       functionKind,
		"status":     status,
	}}
	u.SetName(name)
	u.SetLabels(functionLabels(s))
	return u
}

// sameJSON reports whether a and b encode the same: a status read back
// holds its numbers as int64, the one written as float64.
func sameJSON(a, b map[string]any) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	return err == nil && bytes.Equal(ja, jb)
}

// toMap is s as an unstructured object holds it.
func toMap(s fnstatus.Status) (map[string]any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}
