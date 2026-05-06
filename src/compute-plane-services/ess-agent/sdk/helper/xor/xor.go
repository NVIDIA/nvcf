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

package xor

import (
	"encoding/base64"
	"fmt"
)

// XORBytes takes two byte slices and XORs them together, returning the final
// byte slice. It is an error to pass in two byte slices that do not have the
// same length.
func XORBytes(a, b []byte) ([]byte, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("length of byte slices is not equivalent: %d != %d", len(a), len(b))
	}

	buf := make([]byte, len(a))

	for i := range a {
		buf[i] = a[i] ^ b[i]
	}

	return buf, nil
}

// XORBase64 takes two base64-encoded strings and XORs the decoded byte slices
// together, returning the final byte slice. It is an error to pass in two
// strings that do not have the same length to their base64-decoded byte slice.
func XORBase64(a, b string) ([]byte, error) {
	aBytes, err := base64.StdEncoding.DecodeString(a)
	if err != nil {
		return nil, fmt.Errorf("error decoding first base64 value: %w", err)
	}
	if aBytes == nil || len(aBytes) == 0 {
		return nil, fmt.Errorf("decoded first base64 value is nil or empty")
	}

	bBytes, err := base64.StdEncoding.DecodeString(b)
	if err != nil {
		return nil, fmt.Errorf("error decoding second base64 value: %w", err)
	}
	if bBytes == nil || len(bBytes) == 0 {
		return nil, fmt.Errorf("decoded second base64 value is nil or empty")
	}

	return XORBytes(aBytes, bBytes)
}
