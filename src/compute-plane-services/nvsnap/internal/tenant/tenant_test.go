// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"context"
	"errors"
	"testing"
	"time"

	logrustest "github.com/sirupsen/logrus/hooks/test"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// Namespaces of one tenant share a scope; another tenant, an unlabelled
// namespace and an unconfigured resolver each get their own.
func TestScope(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset(
		ns("sr-a", map[string]string{"nca-id": "acct-1"}),
		ns("sr-b", map[string]string{"nca-id": "acct-1"}),
		ns("sr-c", map[string]string{"nca-id": "acct-2"}),
		ns("plain", nil),
	)
	r := &Resolver{Kube: kc, Label: "nca-id"}
	if a, b := r.Scope(ctx, "sr-a"), r.Scope(ctx, "sr-b"); a != b || a != "tenant:nca-id=acct-1" {
		t.Errorf("one tenant: %q, %q", a, b)
	}
	if c := r.Scope(ctx, "sr-c"); c != "tenant:nca-id=acct-2" {
		t.Errorf("another tenant: %q", c)
	}
	if p := r.Scope(ctx, "plain"); p != "namespace:plain" {
		t.Errorf("unlabelled: %q", p)
	}
	if g := r.Scope(ctx, "gone"); g != "namespace:gone" {
		t.Errorf("unreadable: %q", g)
	}
	var none *Resolver
	if s := none.Scope(ctx, "sr-a"); s != "namespace:sr-a" {
		t.Errorf("nil resolver: %q", s)
	}
	if s := (&Resolver{Kube: kc}).Scope(ctx, "sr-a"); s != "namespace:sr-a" {
		t.Errorf("no label configured: %q", s)
	}
}

// A namespace is read once per TTL; a failed read is not remembered.
func TestScopeCaches(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset(ns("sr-a", map[string]string{"nca-id": "acct-1"}))
	gets := 0
	fail := false
	kc.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if fail {
			return true, nil, errors.New("apiserver down")
		}
		return false, nil, nil
	})
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	r := &Resolver{Kube: kc, Label: "nca-id", TTL: time.Minute, Now: func() time.Time { return now }}
	for i := 0; i < 32; i++ {
		r.Scope(ctx, "sr-a")
	}
	if gets != 1 {
		t.Fatalf("%d namespace reads for 32 lookups, want 1", gets)
	}
	now = now.Add(2 * time.Minute)
	fail = true
	if s := r.Scope(ctx, "sr-a"); s != "namespace:sr-a" {
		t.Errorf("unreadable after expiry: %q", s)
	}
	fail = false
	if s := r.Scope(ctx, "sr-a"); s != "tenant:nca-id=acct-1" {
		t.Errorf("a failed read was remembered: %q", s)
	}
}

// Without namespace read access the resolver falls back to the namespace
// and says so once, not on every lookup.
func TestScopeForbiddenLogsOnce(t *testing.T) {
	ctx := context.Background()
	kc := fake.NewSimpleClientset(ns("sr-a", map[string]string{"nca-id": "acct-1"}))
	kc.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "sr-a", errors.New("rbac"))
	})
	log, hook := logrustest.NewNullLogger()
	r := &Resolver{Kube: kc, Label: "nca-id", Log: log}
	for i := 0; i < 3; i++ {
		if s := r.Scope(ctx, "sr-a"); s != "namespace:sr-a" {
			t.Fatalf("forbidden read: %q", s)
		}
	}
	if n := len(hook.AllEntries()); n != 1 {
		t.Errorf("%d warnings for 3 forbidden lookups, want 1", n)
	}
}
