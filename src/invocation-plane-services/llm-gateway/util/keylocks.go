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

package util

import (
	"sync"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
)

type KeyLocks struct {
	m sync.Map
}

func NewKeyLocks() *KeyLocks {
	return &KeyLocks{}
}

func (kl *KeyLocks) Lock(key any) {
	val, _ := kl.m.LoadOrStore(key, &sync.Mutex{})
	mutex := must.As[*sync.Mutex](val)
	mutex.Lock()
}

func (kl *KeyLocks) Unlock(key any) {
	val, ok := kl.m.Load(key)
	if !ok {
		panic("unlock of unlocked mutex")
	}
	mutex := must.As[*sync.Mutex](val)
	mutex.Unlock()
}
