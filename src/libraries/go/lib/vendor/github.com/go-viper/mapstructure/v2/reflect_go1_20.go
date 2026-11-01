// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build go1.20

package mapstructure

import "reflect"

// TODO: remove once we drop support for Go <1.20
func isComparable(v reflect.Value) bool {
	return v.Comparable()
}
