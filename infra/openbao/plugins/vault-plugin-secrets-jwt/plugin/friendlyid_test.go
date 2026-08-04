// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jwtsecrets

import (
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// decodeBase62 is the test-side inverse. The production code only encodes, so
// this exists to assert the encoding is reversible rather than merely stable.
func decodeBase62(s string) (*big.Int, error) {
	n := new(big.Int)
	base := big.NewInt(int64(len(base62Alphabet)))
	for _, r := range s {
		i := strings.IndexRune(base62Alphabet, r)
		if i < 0 {
			return nil, errBadDigit
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(i)))
	}
	return n, nil
}

var errBadDigit = &digitError{}

type digitError struct{}

func (*digitError) Error() string { return "digit outside the base62 alphabet" }

func TestEncodeBase62UUIDIsFixedWidth(t *testing.T) {
	// A UUID of all zero bytes is the case padding exists for: without it this
	// encodes to "0" and looks nothing like a sibling id.
	got := encodeBase62UUID(uuid.UUID{})
	if len(got) != encodedIDLen {
		t.Fatalf("zero UUID encoded to %d chars, want %d: %q", len(got), encodedIDLen, got)
	}
	if got != strings.Repeat("0", encodedIDLen) {
		t.Errorf("zero UUID = %q, want %q", got, strings.Repeat("0", encodedIDLen))
	}

	// The maximum UUID must still fit in the fixed width, which is what pins
	// encodedIDLen at 22 rather than 21.
	var max uuid.UUID
	for i := range max {
		max[i] = 0xff
	}
	if got := encodeBase62UUID(max); len(got) != encodedIDLen {
		t.Errorf("max UUID encoded to %d chars, want %d: %q", len(got), encodedIDLen, got)
	}
}

func TestEncodeBase62UUIDRoundTrips(t *testing.T) {
	for i := 0; i < 200; i++ {
		id, err := uuid.NewRandom()
		if err != nil {
			t.Fatalf("generating uuid: %v", err)
		}
		enc := encodeBase62UUID(id)
		if len(enc) != encodedIDLen {
			t.Fatalf("%s encoded to %d chars, want %d", id, len(enc), encodedIDLen)
		}
		decoded, err := decodeBase62(enc)
		if err != nil {
			t.Fatalf("decoding %q: %v", enc, err)
		}
		if want := new(big.Int).SetBytes(id[:]); decoded.Cmp(want) != 0 {
			t.Fatalf("%s round-tripped to %s via %q", id, decoded, enc)
		}
	}
}

func TestEncodeBase62UUIDIsDistinct(t *testing.T) {
	// Guards against an encoder that silently truncates: distinct UUIDs must
	// not collapse onto the same id.
	seen := make(map[string]uuid.UUID, 500)
	for i := 0; i < 500; i++ {
		id, err := uuid.NewRandom()
		if err != nil {
			t.Fatalf("generating uuid: %v", err)
		}
		enc := encodeBase62UUID(id)
		if prev, dup := seen[enc]; dup {
			t.Fatalf("collision: %s and %s both encode to %q", prev, id, enc)
		}
		seen[enc] = id
	}
}

func TestEncodeBase62UUIDUsesOnlyAlphabet(t *testing.T) {
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatalf("generating uuid: %v", err)
	}
	for _, r := range encodeBase62UUID(id) {
		if !strings.ContainsRune(base62Alphabet, r) {
			t.Errorf("encoded id contains %q, outside the base62 alphabet", r)
		}
	}
}

func TestFriendlyIDGeneratorProducesUsableIDs(t *testing.T) {
	// The generator is what the plugin actually calls; assert the wiring, not
	// just the helper.
	var gen friendlyIdGenerator
	id, err := gen.id()
	if err != nil {
		t.Fatalf("id(): %v", err)
	}
	if len(id) != encodedIDLen {
		t.Errorf("generator produced %d chars, want %d: %q", len(id), encodedIDLen, id)
	}
}

func TestGeneratedIDsAreNotTimeOrMACDerived(t *testing.T) {
	// A v1 UUID encodes the host MAC and a timestamp, and this id becomes the
	// token's jti. Assert version 4 at the source rather than inspecting the
	// encoded string, which is opaque by design.
	for i := 0; i < 50; i++ {
		id, err := uuid.NewRandom()
		if err != nil {
			t.Fatalf("generating uuid: %v", err)
		}
		if got := id.Version(); got != 4 {
			t.Fatalf("expected a version 4 UUID, got version %d", got)
		}
	}
	// Two ids in a row must not share a suffix: v1 UUIDs end in the node id,
	// which is constant per host.
	var gen friendlyIdGenerator
	a, err := gen.id()
	if err != nil {
		t.Fatalf("id(): %v", err)
	}
	b, err := gen.id()
	if err != nil {
		t.Fatalf("id(): %v", err)
	}
	if a[len(a)-6:] == b[len(b)-6:] {
		t.Errorf("consecutive ids share a trailing segment (%q, %q); ids may be node-derived", a, b)
	}
}
