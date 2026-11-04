// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpointstore

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes/fake"
)

// A record without data is readable by hash, and a later record for the
// same hash from another node adds that node.
func TestRecordManifest_ReadableByHash(t *testing.T) {
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cm := NewConfigMapBackend(local, fake.NewSimpleClientset(), "nvsnap-system", logrus.New())
	chain := &Chain{Members: []Backend{cm}}
	ctx := context.Background()
	hash := "d9a846172fb1663b4e26ea05c4425ab454386b7dd45d5c01bafbd42445439f93"
	m := Manifest{Hash: hash, CaptureMethod: "criu", CapturedOnNodes: []string{"n1"},
		SourcePodMeta: map[string]string{"engine": "criu", "checkpoint_id": "d9a8__20261008-160856"}}
	if err := chain.RecordManifest(ctx, hash, m); err != nil {
		t.Fatal(err)
	}
	m.CapturedOnNodes = []string{"n2"}
	if err := chain.RecordManifest(ctx, hash, m); err != nil {
		t.Fatal(err)
	}
	got, err := chain.Stat(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.CaptureMethod != "criu" || got.SourcePodMeta["checkpoint_id"] != "d9a8__20261008-160856" || len(got.CapturedOnNodes) != 2 {
		t.Errorf("record = %+v", got)
	}
	if err := (&Chain{Members: []Backend{local}}).RecordManifest(ctx, hash, m); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a chain without a record store must say so, got %v", err)
	}
}
