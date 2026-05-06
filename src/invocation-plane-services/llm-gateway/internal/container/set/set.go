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

package set

import (
	"iter"
	"maps"
)

// A Set is a collection of unique values. Given a type T, Set[T] is syntactic
// sugar for map[T]struct{}.
type Set[T comparable] struct {
	data map[T]struct{}
}

// New creates a new [Set[T]] with the given values.
func New[T comparable](values ...T) Set[T] {
	data := make(map[T]struct{}, len(values))
	for _, value := range values {
		data[value] = struct{}{}
	}

	return Set[T]{
		data: data,
	}
}

// Clear clears all members from the set.
func (s Set[T]) Clear() {
	clear(s.data)
}

// Contains returns whether s contains the given value.
func (s Set[T]) Contains(value T) bool {
	_, ok := s.data[value]
	return ok
}

// Delete deletes the given value from s, if present.
func (s Set[T]) Delete(value T) {
	delete(s.data, value)
}

// Insert adds the given value to s if it does not already exist. The boolean
// return indicates whether the value was inserted.
func (s Set[T]) Insert(value T) bool {
	if _, ok := s.data[value]; ok {
		return false
	}
	s.data[value] = struct{}{}
	return true
}

// InsertN adds the given values to s if they do not already exist.
func (s Set[T]) InsertN(values ...T) {
	for i := range values {
		s.data[values[i]] = struct{}{}
	}
}

// Iter returns the contents of the set as an [iter.Seq[T]].
func (s Set[T]) Iter() iter.Seq[T] {
	return maps.Keys(s.data)
}

// Len returns the number of items in the set.
func (s Set[T]) Len() int {
	return len(s.data)
}

// Clone returns a copy of s.
func (s Set[T]) Clone() Set[T] {
	return Set[T]{
		data: maps.Clone(s.data),
	}
}
