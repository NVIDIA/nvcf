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

package prompt

import (
	"github.com/nvidia-lpu/minijinja"
	"github.com/nvidia-lpu/parsec/orderedmap"
)

// OrderedMapMarshaler wraps an *orderedmap.OrderedMap and implements
// minijinja.ObjectMarshaler to allow it to be used in Jinja templates
// while preserving key ordering.
type OrderedMapMarshaler struct {
	m *orderedmap.OrderedMap
}

// WrapOrderedMap wraps an *orderedmap.OrderedMap to make it usable in
// minijinja templates while preserving key ordering. This is necessary
// because minijinja does not natively support the OrderedMap type.
//
// Example usage:
//
//	data := map[string]any{
//		"myOrderedMap": prompt.WrapOrderedMap(orderedMap),
//	}
func WrapOrderedMap(m *orderedmap.OrderedMap) minijinja.ObjectMarshaler {
	if m == nil {
		return nil
	}
	return &OrderedMapMarshaler{m: m}
}

// MarshalJinjaObject implements minijinja.ObjectMarshaler.
func (w *OrderedMapMarshaler) MarshalJinjaObject(enc minijinja.ObjectEncoder) error {
	var err error
	w.m.Range(func(key string, value any) bool {
		// Recursively wrap any nested OrderedMaps
		converted := wrapOrderedMapsRecursive(value)
		err = enc.AddAny(key, converted)
		return err == nil // Continue iteration if no error
	})
	return err
}

// wrapOrderedMapsRecursive recursively walks a value and wraps any
// *orderedmap.OrderedMap instances it finds, regardless of nesting depth.
// This handles OrderedMaps nested in slices, maps, or other OrderedMaps.
func wrapOrderedMapsRecursive(value any) any {
	switch v := value.(type) {
	case *orderedmap.OrderedMap:
		// Wrap the OrderedMap so minijinja can encode it
		return WrapOrderedMap(v)

	case []any:
		if v == nil {
			return nil
		}
		// Recursively process slice elements
		result := make([]any, len(v))
		for i, elem := range v {
			result[i] = wrapOrderedMapsRecursive(elem)
		}
		return result

	case map[string]any:
		if v == nil {
			return nil
		}
		// Recursively process map values
		result := make(map[string]any, len(v))
		for k, val := range v {
			result[k] = wrapOrderedMapsRecursive(val)
		}
		return result

	default:
		// Return as-is for primitive types and unknown types
		return value
	}
}
