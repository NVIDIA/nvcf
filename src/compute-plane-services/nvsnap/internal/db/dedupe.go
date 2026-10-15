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

package db

import "sort"

// looksLikeHash reports whether s is a 64-char lowercase-or-mixed hex string,
// i.e. a sha256 content hash. Used to recover the hash when a catalog row was
// written with the hash in the id column but the hash column left empty (the
// rootfs register path before nvsnap#141 was consistent).
func looksLikeHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// normalizeCheckpointHash backfills c.Hash from c.ID when Hash is empty and ID
// is a content hash. A capture's durable identity is its content hash; the
// rootfs register path keyed the row by hash (id == hash) but didn't always
// populate the Hash column, producing rows that looked hash-less. Normalizing
// here means every row carries its hash so dedupe + UI keying work.
func normalizeCheckpointHash(c *Checkpoint) {
	if c.Hash == "" && looksLikeHash(c.ID) {
		c.Hash = c.ID
	}
}

// checkpointIdentity is the dedupe key: the content hash if known, else the id.
// Two rows for the same capture (e.g. one keyed by NVCA pod-id with the hash
// column set, and one keyed by hash-as-id) collapse to a single entry.
func checkpointIdentity(c Checkpoint) string {
	if c.Hash != "" {
		return c.Hash
	}
	return c.ID
}

// checkpointMoreComplete reports whether a should win over b when both share an
// identity. Preference order: Completed status > others; then larger size;
// then more recently created. This keeps the row that actually has the capture
// metadata (size, completion) over a stub/in-progress duplicate.
func checkpointMoreComplete(a, b Checkpoint) bool {
	ac, bc := a.Status == "Completed", b.Status == "Completed"
	if ac != bc {
		return ac
	}
	if a.CheckpointSize != b.CheckpointSize {
		return a.CheckpointSize > b.CheckpointSize
	}
	return a.CreatedAt.After(b.CreatedAt)
}

// MatchCheckpointByIDOrHash finds the row whose identity matches idOrHash —
// by content hash (after normalizing hash-as-id rows) or by id. Rows are
// hash-normalized first so a detail lookup keyed by the displayed (hash)
// id resolves the same capture the deduped list shows. Returns the
// most-complete matching row, mirroring DedupeCheckpointsByHash so detail
// and list agree. Pure (no DB) for unit testing.
func MatchCheckpointByIDOrHash(rows []Checkpoint, idOrHash string) (Checkpoint, bool) {
	if idOrHash == "" {
		return Checkpoint{}, false
	}
	var best Checkpoint
	found := false
	for i := range rows {
		r := rows[i]
		normalizeCheckpointHash(&r)
		if r.ID != idOrHash && r.Hash != idOrHash {
			continue
		}
		if !found || checkpointMoreComplete(r, best) {
			best = r
			found = true
		}
	}
	return best, found
}

// DedupeCheckpointsByHash collapses rows that share a content identity (hash,
// or id when hash-less), keeping the most-complete row per capture. Input is
// hash-normalized first. Result is sorted newest-first for stable display.
// Pure (no DB) so it is unit-testable in isolation.
func DedupeCheckpointsByHash(rows []Checkpoint) []Checkpoint {
	best := make(map[string]Checkpoint, len(rows))
	order := make([]string, 0, len(rows))
	for i := range rows {
		r := rows[i]
		normalizeCheckpointHash(&r)
		k := checkpointIdentity(r)
		if cur, ok := best[k]; ok {
			if checkpointMoreComplete(r, cur) {
				best[k] = r
			}
			continue
		}
		best[k] = r
		order = append(order, k)
	}
	out := make([]Checkpoint, 0, len(best))
	for _, k := range order {
		out = append(out, best[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}
