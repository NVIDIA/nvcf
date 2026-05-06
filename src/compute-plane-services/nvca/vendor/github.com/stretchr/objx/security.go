// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package objx

import (
	"crypto/sha1"
	"encoding/hex"
)

// HashWithKey hashes the specified string using the security key
func HashWithKey(data, key string) string {
	d := sha1.Sum([]byte(data + ":" + key))
	return hex.EncodeToString(d[:])
}
