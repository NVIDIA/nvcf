// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpointstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// flakyBackend implements only Put and Delete, the methods a chain rollback
// uses: Delete fails deleteFailures times before it succeeds.
type flakyBackend struct {
	Backend
	putErr         error
	deleteFailures int
	deletes        int
}

func (b *flakyBackend) Put(_ context.Context, _ string, _ []CaptureSource, m Manifest) (Manifest, error) {
	if b.putErr != nil {
		return Manifest{}, b.putErr
	}
	m.TotalSizeBytes = 1
	return m, nil
}

func (b *flakyBackend) Delete(context.Context, string) error {
	b.deletes++
	if b.deletes <= b.deleteFailures {
		return errors.New("transient")
	}
	return nil
}

func TestChainRollbackRetriesFailedDeletes(t *testing.T) {
	defer func(d time.Duration) { rollbackBackoff = d }(rollbackBackoff)
	rollbackBackoff = time.Millisecond

	first := &flakyBackend{deleteFailures: 1}
	c := &Chain{Members: []Backend{first, &flakyBackend{putErr: errors.New("promote failed")}}}
	if _, err := c.Put(context.Background(), "h", nil, Manifest{}); err == nil {
		t.Fatal("Put succeeded although a member failed")
	}
	if first.deletes != 2 {
		t.Fatalf("first member deleted %d times, want 2 (one failure, one retry)", first.deletes)
	}

	stuck := &flakyBackend{deleteFailures: 100}
	c = &Chain{Members: []Backend{stuck, &flakyBackend{putErr: errors.New("promote failed")}}, Log: logrus.New()}
	_, _ = c.Put(context.Background(), "h", nil, Manifest{})
	if stuck.deletes != rollbackAttempts {
		t.Fatalf("a member that keeps failing was deleted %d times, want %d", stuck.deletes, rollbackAttempts)
	}
}

func TestConfigMapBackendDeleteReturnsManifestDeleteFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("delete", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	inner := &flakyBackend{}
	b := NewConfigMapBackend(inner, client, "nvsnap-system", logrus.New())
	err := b.Delete(context.Background(), "h")
	if err == nil {
		t.Fatal("a failed manifest delete was reported as success; the stale manifest would read as a capture")
	}
	if inner.deletes != 1 {
		t.Fatalf("inner data deleted %d times, want 1 (the data delete still runs)", inner.deletes)
	}

	// A manifest that is already gone is not a failure.
	ok := NewConfigMapBackend(&flakyBackend{}, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
	if err := ok.Delete(context.Background(), "h"); err != nil {
		t.Fatalf("delete of a hash with no manifest: %v", err)
	}
}
