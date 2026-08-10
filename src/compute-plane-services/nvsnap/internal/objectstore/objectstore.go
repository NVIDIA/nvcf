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

// Package objectstore is the cross-cluster L4 tier: a content-addressed
// blob store backed by managed object storage (GCS today, S3-ready).
//
// A capture's identity is its content hash (checkpointstore.HashInput),
// which is cloud- and cluster-agnostic. So one capture taken on cluster A
// is, by identity, a valid restore source on cluster B — cross-cluster is a
// data-movement problem, not a correctness one. This package moves the
// bytes: A pushes a capture's file tree + manifest to its home bucket; B
// probes a bucket list by hash and pulls on demand, then replays the
// capture commit locally (manifest CM + L2 rox PVC) so the webhook and NVCA
// treat it exactly like a native local capture.
//
// See docs/design/cross-cluster-replication.md.
package objectstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Head/Get when the object (or a capture's
// manifest) is absent. Callers probing a bucket list treat this as "miss,
// try the next bucket" rather than a hard error.
var ErrNotFound = errors.New("objectstore: object not found")

// ObjectInfo is the metadata a Head returns for one object.
type ObjectInfo struct {
	Key  string
	Size int64
}

// Bucket is one object-storage bucket (a GCS bucket, an S3 bucket).
// Keys are relative paths within the bucket; the caller composes the
// "<hash>/..." layout. Implementations must be safe for concurrent use.
type Bucket interface {
	// Name is the bucket identifier for logging ("GCP-H100-a-captures").
	Name() string

	// Head returns ObjectInfo for key, or ErrNotFound if absent.
	Head(ctx context.Context, key string) (ObjectInfo, error)

	// List returns every object whose key starts with prefix. Used by
	// the pull path to enumerate a capture's tree (the manifest records
	// volumes, not individual files). Returns an empty slice (not
	// ErrNotFound) when nothing matches.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// Get opens key for reading. The caller must Close the reader.
	// Returns ErrNotFound if the object is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Put writes size bytes from r to key. size may be -1 if unknown.
	// Overwrites any existing object at key (content-addressed writes are
	// idempotent; the bytes for a given content hash never change).
	Put(ctx context.Context, key string, r io.Reader, size int64) error
}
