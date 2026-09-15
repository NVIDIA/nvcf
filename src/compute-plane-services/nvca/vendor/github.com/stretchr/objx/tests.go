// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package objx

// Has gets whether there is something at the specified selector
// or not.
//
// If m is nil, Has will always return false.
func (m Map) Has(selector string) bool {
	if m == nil {
		return false
	}
	return !m.Get(selector).IsNil()
}

// IsNil gets whether the data is nil or not.
func (v *Value) IsNil() bool {
	return v == nil || v.data == nil
}
