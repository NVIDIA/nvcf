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

package keysutil

import lru "github.com/hashicorp/golang-lru"

type TransitLRU struct {
	size int
	lru  *lru.TwoQueueCache
}

func NewTransitLRU(size int) (*TransitLRU, error) {
	lru, err := lru.New2Q(size)
	return &TransitLRU{lru: lru, size: size}, err
}

func (c *TransitLRU) Delete(key interface{}) {
	c.lru.Remove(key)
}

func (c *TransitLRU) Load(key interface{}) (value interface{}, ok bool) {
	return c.lru.Get(key)
}

func (c *TransitLRU) Store(key, value interface{}) {
	c.lru.Add(key, value)
}

func (c *TransitLRU) Size() int {
	return c.size
}
