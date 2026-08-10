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

// ChainBackend composes N Backend implementations into a single Backend.
// Used by the rootfs capture path to fan a single Put out across:
//
//   - LocalBackend            — host hostPath cache (per-node, restore-fast)
//   - ConfigMapBackend        — manifest CM (cluster-wide lookup index)
//   - PerCapturePVCBackend    — rwx → snapshot → rox PVC (cluster-wide fan-out
//                               for N restored pods mounting one read-only disk)
//
// See docs/design/ROOTFS-EVERYWHERE.md.

package checkpointstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Chain is a composite Backend that fans Put out across multiple
// implementations and dispatches Mount to the most-restore-friendly one.
//
// Members are stored in order. Put runs sequentially; on failure of any
// member after the first, prior successful members get a best-effort
// rollback Delete. Get/Stat try members first-to-last and return the first
// hit. Mount tries members last-to-first because the most-recently-added
// member is the most restore-friendly (PerCapturePVCBackend's rox PVC works
// across the cluster vs LocalBackend's per-node hostPath).
type Chain struct {
	Members []Backend
	Log     logrus.FieldLogger
}

var _ Backend = (*Chain)(nil)

// Put fans out across all members in order. Returns the manifest from the
// first member that successfully populated SizeBytes / FileCount; rolls
// back successful members on later failure.
func (c *Chain) Put(ctx context.Context, hash string, sources []CaptureSource, m Manifest) (Manifest, error) {
	ctx, span := tracing.Tracer().Start(ctx, "store.chain_put")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.hash", ShortHash(hash)),
		attribute.Int("nvsnap.members", len(c.Members)),
		attribute.Int("nvsnap.sources", len(sources)),
	)
	if len(c.Members) == 0 {
		return Manifest{}, errors.New("checkpointstore.Chain: no members")
	}

	var primary Manifest
	completed := 0
	for i, b := range c.Members {
		stored, err := b.Put(ctx, hash, sources, m)
		if err != nil {
			if errors.Is(err, ErrExists) {
				// Idempotent: an earlier capture already populated this
				// member. Continue with the next; don't roll back.
				if c.Log != nil {
					c.Log.WithField("member_index", i).Debug("chain.Put: member already has hash, continuing")
				}
				continue
			}
			c.rollback(ctx, hash, completed)
			return Manifest{}, fmt.Errorf("chain member %d: %w", i, err)
		}
		completed++
		// First successful Put with non-zero SizeBytes is the authoritative
		// manifest for the chain — subsequent members copy from those same
		// sources and produce identical numbers.
		if primary.TotalSizeBytes == 0 && stored.TotalSizeBytes > 0 {
			primary = stored
		}
	}
	if primary.TotalSizeBytes == 0 {
		// All members reported ErrExists. Return the manifest as-given;
		// caller can Stat to fetch the existing one.
		return m, ErrExists
	}
	return primary, nil
}

// rollback best-effort deletes a hash from the first n members. Used when
// a later Put fails and the chain needs to leave the catalog clean.
func (c *Chain) rollback(ctx context.Context, hash string, n int) {
	for i := 0; i < n; i++ {
		if err := c.Members[i].Delete(ctx, hash); err != nil && !errors.Is(err, ErrNotFound) {
			if c.Log != nil {
				c.Log.WithError(err).WithField("member_index", i).Warn("chain.Put: rollback Delete failed; orphan-GC will catch this later")
			}
		}
	}
}

// Get tries members in order. First hit wins. The expected ordering puts
// LocalBackend first (fastest cache-hit path).
func (c *Chain) Get(ctx context.Context, hash, dstDir string) (Manifest, error) {
	ctx, span := tracing.Tracer().Start(ctx, "store.chain_get")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", ShortHash(hash)))
	var lastErr error
	for _, b := range c.Members {
		m, err := b.Get(ctx, hash, dstDir)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Manifest{}, err
		}
		lastErr = err
	}
	return Manifest{}, lastErr
}

// Stat tries members in order. First hit wins; same ordering rationale as Get.
func (c *Chain) Stat(ctx context.Context, hash string) (Manifest, error) {
	var lastErr error
	for _, b := range c.Members {
		m, err := b.Stat(ctx, hash)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Manifest{}, err
		}
		lastErr = err
	}
	return Manifest{}, lastErr
}

// Delete fans out best-effort across all members. Returns ErrNotFound only
// when no member had the hash. Other errors are logged and returned (caller
// can decide whether to retry).
func (c *Chain) Delete(ctx context.Context, hash string) error {
	allMissing := true
	var firstErr error
	for i, b := range c.Members {
		err := b.Delete(ctx, hash)
		if err == nil {
			allMissing = false
			continue
		}
		if errors.Is(err, ErrNotFound) {
			continue
		}
		allMissing = false
		if firstErr == nil {
			firstErr = fmt.Errorf("chain member %d delete: %w", i, err)
		}
		if c.Log != nil {
			c.Log.WithError(err).WithField("member_index", i).Warn("chain.Delete: member delete failed")
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if allMissing {
		return ErrNotFound
	}
	return nil
}

// Mount tries members last-to-first. The convention is that more
// restore-friendly backends are appended later in the chain:
// PerCapturePVCBackend's rox PVC works cluster-wide and is preferred over
// LocalBackend's per-node hostPath. ErrNotFound from one member falls
// through to the next; any other error is returned immediately because
// it's likely a configuration issue.
func (c *Chain) Mount(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	ctx, span := tracing.Tracer().Start(ctx, "store.chain_mount")
	defer span.End()
	span.SetAttributes(
		attribute.String("nvsnap.hash", ShortHash(hash)),
		attribute.String("nvsnap.volume", vol.Name),
	)
	var lastErr error
	for i := len(c.Members) - 1; i >= 0; i-- {
		mp, err := c.Members[i].Mount(ctx, hash, vol)
		if err == nil {
			return mp, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return PodMount{}, err
		}
		lastErr = err
	}
	return PodMount{}, lastErr
}
