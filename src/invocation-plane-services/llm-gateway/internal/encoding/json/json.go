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

package json

import (
	"bytes"
	"unsafe"
)

var (
	_nullByte    = []byte("\\u0000")
	_encoderPool = NewBufferedEncoderPool(0)
)

// UnmarshalString is a wrapper around [Unmarshal] that unmarshals the given
// string's underlying bytes into dst.
func UnmarshalString(data string, dst any) error {
	return Unmarshal(unsafe.Slice(unsafe.StringData(data), len(data)), dst)
}

// MarshalJSONBSafe is a wrapper around [Marshal] that replaces bytes that are invalid
// when encoding into JSONB columns.
func MarshalJSONBSafe(v any) ([]byte, error) {
	enc := _encoderPool.Get()
	defer _encoderPool.Put(enc)

	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	// n.b. TrimmedBytes() will return a slice of enc's underlying byte buffer as
	// live storage. This is OK because `bytes.ReplaceAll` guarantees a copy
	// of the passed data, which in turn guarantees that we're not leaking
	// pooled storage to the caller.
	safeData := bytes.ReplaceAll(enc.TrimmedBytes(), _nullByte, nil)

	// Attempt to decode the resulting JSON to ensure it's valid.
	// If it's not, we just return an error and move on.
	var (
		dec = NewDecoder(bytes.NewReader(safeData))
		obj any
	)

	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}

	return safeData, nil
}
