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

// Package ptr provides utilities for interacting with pointers.
package ptr

import (
	"cmp"
	"errors"
)

// To returns a pointer to v.
func To[T any](v T) *T {
	return &v
}

// ToNonZero returns a pointer to v if v is not the zero value, otherwise returns nil.
func ToNonZero[T comparable](v T) *T {
	var zero T
	if v != zero {
		return &v
	}
	return nil
}

// Deref returns the value pointed to by v if v is non-nil, otherwise returns a
// zero value of T.
func Deref[T any](v *T) T {
	var zero T
	return DerefOr(v, zero)
}

// DerefOr returns the value pointed to by v if v is non-nil, otherwise returns
// the given fallback.
func DerefOr[T any](v *T, fallback T) T {
	if v != nil {
		return *v
	}
	return fallback
}

// DerefOrElse returns the value pointed to by v if v is non-nil, otherwise
// returns the value produced by fn.
func DerefOrElse[T any](v *T, fn func() T) T {
	if v != nil {
		return *v
	}
	return fn()
}

// If calls fn with v if v is non-nil, returning the produced value.
func If[In any, Out any](v *In, fn func(*In) Out) Out {
	if v == nil {
		var zero Out
		return zero
	}
	return fn(v)
}

// DerefIf calls fn with the dereferenced value of v if v is non-nil, returning
// whether fn was called.
func DerefIf[T any](v *T, fn func(T)) bool {
	if v == nil {
		return false
	}
	fn(*v)
	return true
}

// Eq returns whether v is not nil and is equal to want.
func Eq[T comparable](v *T, want T) bool {
	if v == nil {
		return false
	}
	return *v == want
}

// Ne returns whether v is nil or is not equal to want.
func Ne[T comparable](v *T, want T) bool {
	if v == nil {
		return true
	}
	return *v != want
}

// Gt returns whether v is not nil and is lexicographically greater than want.
func Gt[T cmp.Ordered](v *T, want T) bool {
	if v == nil {
		return false
	}
	return *v > want
}

// Gte returns whether v is not nil and is lexicographically greater than or
// equal to want.
func Gte[T cmp.Ordered](v *T, want T) bool {
	if v == nil {
		return false
	}
	return *v >= want
}

// Lt returns whether v is not nil and is lexicographically less than want.
func Lt[T cmp.Ordered](v *T, want T) bool {
	if v == nil {
		return false
	}
	return *v < want
}

// Lte returns whether v is not nil and is lexicographically less than or equal
// to want.
func Lte[T cmp.Ordered](v *T, want T) bool {
	if v == nil {
		return false
	}
	return *v <= want
}

// Nop satisfies a callable func when there is no desired effect.
func Nop() {}

// ErrAs checks if err is of type T and returns it if so.
func ErrAs[T error](err error) (T, bool) {
	var target T
	if errors.As(err, &target) {
		return target, true
	}
	return target, false
}

// ErrIs checks if err is of type T.
func ErrIs[T error](err error) bool {
	_, ok := ErrAs[T](err)
	return ok
}
