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

// Package lastcluster remembers which Stargate cluster served each session's
// last successful request so the gateway can send it back to Stargate as the
// x-stargate-last-cluster-id hint.
package lastcluster

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"time"
)

const (
	// DMapName is the Olric DMap that holds last-cluster entries. It is
	// separate from the rate-limit DMap.
	DMapName = "stargate-last-cluster"
	// MaxValueBytes bounds the stored cluster ID. Longer values are not stored.
	MaxValueBytes = 256

	keyPrefix = "lc:v1:"
)

// Store holds session key to cluster ID entries with a per-entry TTL.
type Store interface {
	// Get returns ok=false when the key is absent or expired.
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	// Set writes the value and resets the entry TTL.
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

// Key derives the store key for a session. Each part is length-prefixed
// before hashing so different splits of the same bytes never collide, and raw
// routing keys or session identifiers never appear in the store key.
func Key(routingKey, model, cacheAffinityKey string) string {
	h := sha256.New()
	var n [8]byte
	for _, part := range []string{routingKey, model, cacheAffinityKey} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		_, _ = h.Write(n[:])
		_, _ = io.WriteString(h, part)
	}
	return keyPrefix + hex.EncodeToString(h.Sum(nil))
}

// validClusterID reports whether a cluster ID may be stored and later sent as
// a request header value: non-empty, at most MaxValueBytes, and printable
// ASCII only.
func validClusterID(value string) bool {
	if value == "" || len(value) > MaxValueBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
