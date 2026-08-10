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

package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

// GCSBucket is an objectstore.Bucket backed by one Google Cloud Storage
// bucket. Auth is via Application Default Credentials (Workload Identity on
// GKE) — no key material in the binary.
type GCSBucket struct {
	client *storage.Client
	bucket string
}

// Client is created once per process and shared across buckets; the GCS
// client is safe for concurrent use and pools HTTP connections.
type Client struct {
	client *storage.Client
}

// NewGCS opens a GCS client (ADC) and returns a closer plus a per-bucket
// factory. The closer must be called on shutdown to release the client.
//
//	cl, closeFn, err := NewGCS(ctx)
//	home := cl.Bucket("GCP-H100-a-captures")
func NewGCS(ctx context.Context) (*Client, func() error, error) {
	c, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("objectstore: open GCS client: %w", err)
	}
	return &Client{client: c}, c.Close, nil
}

// Bucket returns a Bucket handle for name. Cheap — does not perform I/O.
func (g *Client) Bucket(name string) Bucket {
	return &GCSBucket{client: g.client, bucket: name}
}

// Name returns the bucket identifier (used for logging).
func (b *GCSBucket) Name() string { return b.bucket }

// Head returns ObjectInfo for key, or ErrNotFound if the object is absent.
func (b *GCSBucket) Head(ctx context.Context, key string) (ObjectInfo, error) {
	attrs, err := b.client.Bucket(b.bucket).Object(key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, fmt.Errorf("objectstore: head gs://%s/%s: %w", b.bucket, key, err)
	}
	return ObjectInfo{Key: key, Size: attrs.Size}, nil
}

// List returns every object whose key starts with prefix, or an empty slice
// when nothing matches.
func (b *GCSBucket) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	it := b.client.Bucket(b.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("objectstore: list gs://%s/%s: %w", b.bucket, prefix, err)
		}
		out = append(out, ObjectInfo{Key: attrs.Name, Size: attrs.Size})
	}
	return out, nil
}

// Get opens key for reading; the caller must Close the reader. Returns
// ErrNotFound if the object is absent.
func (b *GCSBucket) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := b.client.Bucket(b.bucket).Object(key).NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("objectstore: get gs://%s/%s: %w", b.bucket, key, err)
	}
	return r, nil
}

// Put writes size bytes from r to key, overwriting any existing object.
func (b *GCSBucket) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	w := b.client.Bucket(b.bucket).Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("objectstore: put gs://%s/%s: %w", b.bucket, key, err)
	}
	if err := w.Close(); err != nil {
		// A 412 here means a concurrent writer landed the same content-
		// addressed object first; that's benign (the bytes are identical).
		var ae *googleapi.Error
		if errors.As(err, &ae) && ae.Code == 412 {
			return nil
		}
		return fmt.Errorf("objectstore: finalize gs://%s/%s: %w", b.bucket, key, err)
	}
	return nil
}
