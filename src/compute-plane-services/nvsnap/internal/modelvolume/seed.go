/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package modelvolume

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Agent-served cache seeding: instead of attaching a read-only cache
// volume to every pod, the agent on each node keeps one local mirror of
// the cache per key and hands it to pods over its API. A pod fetches only
// the keys the webhook admitted it for: the webhook signs (namespace,
// cache URI) with the agent token and the agent verifies the same
// signature, so no token is distributed to workload namespaces.
const (
	// SeedTokenHeader carries the per-pod seed signature.
	SeedTokenHeader = "X-Nvsnap-Seed-Token"
	// SeedMirrorMarker at a mirror root says the copy from the cache
	// volume finished.
	SeedMirrorMarker = ".nvsnap-complete"
	// SeedLastUsedFile at a mirror root is touched on every serve; the
	// mirror sweep retires mirrors unused past retention.
	SeedLastUsedFile = ".nvsnap-last-used"
	// SeedModeVolume attaches the read-only cache volume to each pod (the
	// original mechanism); SeedModeAgent fetches from the node agent.
	SeedModeVolume = "volume"
	SeedModeAgent  = "agent"
)

// SeedToken is the signature a pod in ns presents to fetch the cache uri.
// An empty secret still yields a stable value so a deployment without
// agent auth keeps working; it then only binds the request to its key.
func SeedToken(secret, ns, uri string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ns))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(uri))
	return hex.EncodeToString(mac.Sum(nil))
}

// SeedTokenValid compares a presented signature in constant time.
func SeedTokenValid(secret, ns, uri, presented string) bool {
	return presented != "" && hmac.Equal([]byte(presented), []byte(SeedToken(secret, ns, uri)))
}
