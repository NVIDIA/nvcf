// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package tenant names the tenant a namespace belongs to. A capture or a
// cache is filled with the credentials and data of the tenant that produced
// it, so it is shared only within that tenant: the tenant is part of every
// content hash.
package tenant

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Resolver names the tenant of a namespace: the value of the namespace's
// Label (one the platform stamps and namespace users cannot edit, such as an
// account id), else the namespace itself. A namespace that cannot be read is
// its own tenant: what is not known to be shared is not shared.
type Resolver struct {
	Kube  kubernetes.Interface
	Label string
	// Log, if set, says once when namespaces cannot be read at all
	// (missing RBAC): the tenant label then has no effect.
	Log logrus.FieldLogger
	// TTL is how long a namespace's tenant is remembered; 0 is a minute.
	// An instance of tens of pods resolves its namespace once, not per pod.
	TTL time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu        sync.Mutex
	cache     map[string]entry
	forbidden sync.Once
}

type entry struct {
	scope string
	until time.Time
}

// Scope returns the tenant scope of namespace ns: "tenant:<label>=<value>"
// when the label is configured and the namespace carries it, else
// "namespace:<ns>".
func (r *Resolver) Scope(ctx context.Context, ns string) string {
	own := "namespace:" + ns
	if r == nil || r.Label == "" || r.Kube == nil || ns == "" {
		return own
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	r.mu.Lock()
	if e, ok := r.cache[ns]; ok && now.Before(e.until) {
		r.mu.Unlock()
		return e.scope
	}
	r.mu.Unlock()
	scope := own
	n, err := r.Kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) && r.Log != nil {
			r.forbidden.Do(func() {
				r.Log.WithError(err).WithField("label", r.Label).
					Warn("tenant: cannot read namespaces (RBAC); every namespace is its own tenant and the tenant label has no effect")
			})
		}
		// Not remembered: the next lookup tries again.
		return own
	}
	if v := n.Labels[r.Label]; v != "" {
		scope = "tenant:" + r.Label + "=" + v
	}
	ttl := r.TTL
	if ttl == 0 {
		ttl = time.Minute
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]entry{}
	}
	r.cache[ns] = entry{scope: scope, until: now.Add(ttl)}
	r.mu.Unlock()
	return scope
}
