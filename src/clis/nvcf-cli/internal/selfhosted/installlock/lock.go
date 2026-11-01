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

// Package installlock provides a Kubernetes Lease-based orchestrator-level lock
// that serialises concurrent `nvcf-cli self-hosted up` invocations targeting
// the same control-plane context + namespace prefix. Without the lock two
// concurrent invocations would race into helmfile apply, producing
// non-deterministic Cassandra row duplication in the SIS cluster_oidc_by_cluster_id
// table and stale register-values YAML files (T6 failure mode per §8.4).
//
// Usage:
//
//	lock := installlock.NewLock(kube, lockKey(ncaID, clusterName), installlock.Options{})
//	if err := lock.Acquire(ctx); err != nil {
//	    if errors.Is(err, installlock.ErrAlreadyHeld) { /* surface to user */ }
//	    /* other errors: best-effort, proceed without lock */
//	}
//	defer lock.Release(context.Background())
package installlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Default config values — all overridable via Options.
const (
	DefaultNamespace   = "nvcf-cli-system"
	DefaultLeaseTTL    = 10 * time.Minute
	DefaultRenewPeriod = 30 * time.Second
)

// ErrAlreadyHeld signals that the lease is held by another process. The error
// message includes the holder's identity (hostname + PID + start-time) so the
// operator can distinguish "another CLI invocation" from "a stale lease".
var ErrAlreadyHeld = errors.New("install lock already held")

// HolderIdentity uniquely identifies a CLI process holding the lock.
//
// The string form is "{hostname} pid={pid} started={rfc3339}" — embedded
// verbatim in user-facing error messages so operators can grep for it across
// nvcf-cli logs.
type HolderIdentity struct {
	Hostname string
	PID      int
	Started  time.Time
}

// String returns the canonical serialised form stored in the Lease's
// HolderIdentity field and surfaced in ErrAlreadyHeld messages.
func (h HolderIdentity) String() string {
	return fmt.Sprintf("%s pid=%d started=%s", h.Hostname, h.PID, h.Started.UTC().Format(time.RFC3339))
}

// CurrentHolder returns the identity for the currently running process. This
// is the default holder used when callers do not supply one in Options.
func CurrentHolder() HolderIdentity {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	return HolderIdentity{
		Hostname: host,
		PID:      os.Getpid(),
		Started:  time.Now().UTC(),
	}
}

// Options configures Lock behaviour. All fields have sensible defaults.
type Options struct {
	// Namespace is the Kubernetes namespace where the Lease object is created.
	// Defaults to DefaultNamespace ("nvcf-cli-system").
	Namespace string

	// LeaseTTL is how long the lease is considered valid after its last renewal.
	// Defaults to DefaultLeaseTTL (10 minutes). Expired leases are re-acquirable
	// by a new holder — this is the crash-recovery path.
	LeaseTTL time.Duration

	// RenewPeriod is how frequently the background renewer refreshes the lease's
	// RenewTime. Should be well under LeaseTTL. Defaults to DefaultRenewPeriod (30s).
	RenewPeriod time.Duration

	// Holder is the identity stamped into the Lease. Defaults to CurrentHolder()
	// (hostname + current PID + now).
	Holder HolderIdentity

	// NowFunc is a clock seam for testing. Defaults to func() time.Time { return time.Now().UTC() }.
	NowFunc func() time.Time
}

// Lock owns the Lease lifecycle for a single key. Construct with NewLock,
// then call Acquire once and defer Release.
type Lock struct {
	kube        kubernetes.Interface
	key         string
	namespace   string
	leaseTTL    time.Duration
	renewPeriod time.Duration
	holder      HolderIdentity
	nowFunc     func() time.Time

	cancelRenew context.CancelFunc
	renewDone   chan struct{}
}

// NewLock constructs a Lock for the given key. The key is typically
// "<ncaID>--<clusterName>" so that two `up` invocations against the same NCA
// account + cluster name conflict. Keys are safe-encoded before use as
// Kubernetes object names.
func NewLock(kube kubernetes.Interface, key string, opts Options) *Lock {
	ns := opts.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	ttl := opts.LeaseTTL
	if ttl == 0 {
		ttl = DefaultLeaseTTL
	}
	renew := opts.RenewPeriod
	if renew == 0 {
		renew = DefaultRenewPeriod
	}
	holder := opts.Holder
	if holder.Hostname == "" {
		holder = CurrentHolder()
	}
	now := opts.NowFunc
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Lock{
		kube:        kube,
		key:         key,
		namespace:   ns,
		leaseTTL:    ttl,
		renewPeriod: renew,
		holder:      holder,
		nowFunc:     now,
	}
}

// Acquire tries to claim the Kubernetes Lease. Returns nil on success.
// Returns ErrAlreadyHeld (wrapped with the holder identity string) when another
// process holds the lease. Other errors indicate infrastructure problems;
// callers may treat them as best-effort and proceed without the lock.
//
// On success a background renewer goroutine is started to refresh the lease
// every RenewPeriod. Call Release to stop the renewer and delete the lease.
func (l *Lock) Acquire(ctx context.Context) error {
	leaseName := encodeLeaseName(l.key)

	// Ensure the namespace exists (best-effort; non-fatal if creation fails due
	// to RBAC or if the namespace already exists).
	_ = l.ensureNamespace(ctx)

	// Attempt to create the Lease object. If it succeeds we hold the lock.
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: l.namespace,
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       ptrString(l.holder.String()),
			LeaseDurationSeconds: ptrInt32(int32(l.leaseTTL.Seconds())),
			AcquireTime:          &metav1.MicroTime{Time: l.nowFunc()},
			RenewTime:            &metav1.MicroTime{Time: l.nowFunc()},
		},
	}
	_, err := l.kube.CoordinationV1().Leases(l.namespace).Create(ctx, lease, metav1.CreateOptions{})
	if err == nil {
		l.startRenewer()
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create lease: %w", err)
	}

	// Lease exists; fetch it to inspect expiry and holder.
	existing, getErr := l.kube.CoordinationV1().Leases(l.namespace).Get(ctx, leaseName, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("get lease: %w", getErr)
	}

	// If the lease is expired (RenewTime + LeaseDuration < now), take it over.
	// This is the crash-recovery path: the previous holder died without releasing.
	if l.isExpired(existing) {
		existing.Spec.HolderIdentity = ptrString(l.holder.String())
		existing.Spec.AcquireTime = &metav1.MicroTime{Time: l.nowFunc()}
		existing.Spec.RenewTime = &metav1.MicroTime{Time: l.nowFunc()}
		if existing.Spec.LeaseDurationSeconds == nil {
			existing.Spec.LeaseDurationSeconds = ptrInt32(int32(l.leaseTTL.Seconds()))
		}
		_, updErr := l.kube.CoordinationV1().Leases(l.namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if updErr != nil {
			return fmt.Errorf("take over expired lease: %w", updErr)
		}
		l.startRenewer()
		return nil
	}

	// Lease is live and held by someone else.
	holder := derefString(existing.Spec.HolderIdentity)
	return fmt.Errorf("%w (holder: %s)", ErrAlreadyHeld, holder)
}

// Release stops the background renewer goroutine and deletes the Lease object.
// Safe to call even if Acquire was never called or returned an error (no-op in
// that case). Suppresses NotFound errors (idempotent).
func (l *Lock) Release(ctx context.Context) error {
	if l.cancelRenew != nil {
		l.cancelRenew()
		<-l.renewDone
		l.cancelRenew = nil
	}
	leaseName := encodeLeaseName(l.key)
	err := l.kube.CoordinationV1().Leases(l.namespace).Delete(ctx, leaseName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete lease: %w", err)
	}
	return nil
}

// startRenewer launches the background renewer goroutine. Must only be called
// once per successful Acquire.
func (l *Lock) startRenewer() {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancelRenew = cancel
	l.renewDone = make(chan struct{})
	go func() {
		defer close(l.renewDone)
		ticker := time.NewTicker(l.renewPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = l.renew(ctx)
			}
		}
	}()
}

// renew updates the lease's RenewTime to extend the TTL. Called by the
// background renewer goroutine. Errors are ignored by the caller; if renewal
// fails persistently the lease will eventually expire and another process may
// take over.
func (l *Lock) renew(ctx context.Context) error {
	leaseName := encodeLeaseName(l.key)
	existing, err := l.kube.CoordinationV1().Leases(l.namespace).Get(ctx, leaseName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if derefString(existing.Spec.HolderIdentity) != l.holder.String() {
		return errors.New("lease no longer held by this process")
	}
	existing.Spec.RenewTime = &metav1.MicroTime{Time: l.nowFunc()}
	_, err = l.kube.CoordinationV1().Leases(l.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ensureNamespace creates the lock namespace if it does not exist. Errors are
// non-fatal — the Acquire will simply fail at the Create step if the namespace
// is truly missing and RBAC prevents creation.
func (l *Lock) ensureNamespace(ctx context.Context) error {
	_, err := l.kube.CoreV1().Namespaces().Get(ctx, l.namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = l.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: l.namespace},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// isExpired returns true when the lease's RenewTime + LeaseDuration has passed
// according to l.nowFunc. A nil RenewTime is treated as expired (stale object).
func (l *Lock) isExpired(lease *coordv1.Lease) bool {
	if lease.Spec.RenewTime == nil {
		return true
	}
	if lease.Spec.LeaseDurationSeconds == nil {
		return false
	}
	expiresAt := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return l.nowFunc().After(expiresAt)
}

// encodeLeaseName converts an arbitrary key string to a DNS-label-safe
// Kubernetes object name (lowercase alphanum + hyphens, max 253 chars).
// The "nvcf-cli-up-" prefix namespaces the Lease within the nvcf-cli-system
// namespace and avoids conflicts with other Kubernetes Lease users
// (leader-election, etc.).
func encodeLeaseName(key string) string {
	safe := make([]byte, 0, len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			safe = append(safe, byte(r))
		case r >= '0' && r <= '9':
			safe = append(safe, byte(r))
		case r == '-':
			safe = append(safe, '-')
		case r >= 'A' && r <= 'Z':
			safe = append(safe, byte(r+32)) // toLower
		default:
			safe = append(safe, '-')
		}
	}
	const prefix = "nvcf-cli-up-"
	result := prefix + string(safe)
	if len(result) > 253 {
		result = result[:253]
	}
	return result
}

func ptrString(s string) *string { return &s }
func ptrInt32(n int32) *int32    { return &n }
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
