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

	"github.com/google/uuid"
)

// base62Alphabet is the conventional base62 digit set, ordered so that the
// digit value equals the index: 0-9, then A-Z, then a-z. friendly-id uses this
// same ordering, so ids produced here sort and compare like the ones the plugin
// produced before.
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// encodedIDLen is the fixed width of an encoded id. 2^128 needs
// ceil(128 / log2(62)) = 22 base62 digits, so every id is padded to 22 to keep
// the width uniform; without padding a UUID with leading zero bytes would
// encode shorter than its peers and read as a different kind of identifier.
const encodedIDLen = 22

// encodeBase62UUID renders a UUID as a fixed-width base62 string.
//
// The UUID is treated as a single unsigned 128-bit big-endian integer and
// repeatedly divided by 62, most significant digit first. This is the standard
// friendly-id construction and is written here from that definition so the
// plugin carries no dependency on an unlicensed third-party implementation.
func encodeBase62UUID(id uuid.UUID) string {
	n := new(big.Int).SetBytes(id[:])
	base := big.NewInt(int64(len(base62Alphabet)))
	rem := new(big.Int)

	// Filled back to front: division yields the least significant digit first.
	out := make([]byte, encodedIDLen)
	for i := encodedIDLen - 1; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		out[i] = base62Alphabet[rem.Int64()]
	}
	return string(out)
}
