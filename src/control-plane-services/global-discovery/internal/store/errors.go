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

package store

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound reports that a record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrLeaseEpochMismatch reports a heartbeat with an epoch other than the
	// current lease epoch. The writer must register again.
	ErrLeaseEpochMismatch = errors.New("store: lease epoch mismatch")
	// ErrPoolNameTaken reports that another pool of the same
	// (clusterset, cluster) holds the name.
	ErrPoolNameTaken = errors.New("store: pool name taken")
	// ErrTooLarge reports a record above a store limit.
	ErrTooLarge = errors.New("store: record too large")
	// ErrUnavailable reports that the backend cannot be reached or the store
	// is closed. Callers may retry.
	ErrUnavailable = errors.New("store: unavailable")
)

// ErrStaleGeneration reports a conditional put whose generation does not
// exceed the stored one. Current is the stored generation. Match it with
// errors.As, or with errors.Is(err, ErrStaleGeneration{}) when Current does
// not matter.
type ErrStaleGeneration struct {
	Current uint64
}

func (e ErrStaleGeneration) Error() string {
	return fmt.Sprintf("store: stale generation, current generation is %d", e.Current)
}

// Is matches any ErrStaleGeneration regardless of Current.
func (e ErrStaleGeneration) Is(target error) bool {
	_, ok := target.(ErrStaleGeneration)
	return ok
}

// StaleGeneration returns the stored generation of an ErrStaleGeneration in
// err's chain.
func StaleGeneration(err error) (current uint64, ok bool) {
	var stale ErrStaleGeneration
	if errors.As(err, &stale) {
		return stale.Current, true
	}
	return 0, false
}
