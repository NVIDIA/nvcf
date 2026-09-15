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

package locksutil

import (
	"sync"

	"github.com/hashicorp/vault/sdk/helper/cryptoutil"
)

const (
	LockCount = 256
)

type LockEntry struct {
	sync.RWMutex
}

// CreateLocks returns an array so that the locks can be iterated over in
// order.
//
// This is only threadsafe if a process is using a single lock, or iterating
// over the entire lock slice in order. Using a consistent order avoids
// deadlocks because you can never have the following:
//
// Lock A, Lock B
// Lock B, Lock A
//
// Where process 1 is now deadlocked trying to lock B, and process 2 deadlocked trying to lock A
func CreateLocks() []*LockEntry {
	ret := make([]*LockEntry, LockCount)
	for i := range ret {
		ret[i] = new(LockEntry)
	}
	return ret
}

func LockIndexForKey(key string) uint8 {
	return uint8(cryptoutil.Blake2b256Hash(key)[0])
}

func LockForKey(locks []*LockEntry, key string) *LockEntry {
	return locks[LockIndexForKey(key)]
}

func LocksForKeys(locks []*LockEntry, keys []string) []*LockEntry {
	lockIndexes := make(map[uint8]struct{}, len(keys))
	for _, k := range keys {
		lockIndexes[LockIndexForKey(k)] = struct{}{}
	}

	locksToReturn := make([]*LockEntry, 0, len(keys))
	for i, l := range locks {
		if _, ok := lockIndexes[uint8(i)]; ok {
			locksToReturn = append(locksToReturn, l)
		}
	}

	return locksToReturn
}
