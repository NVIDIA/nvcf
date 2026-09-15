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
package pool

import (
	"sync"
)

const bufferSize = 1024 * 32

var ByteSlice = &ByteSlicePool{p: sync.Pool{New: func() any {
	return make([]byte, bufferSize)
}}}

type ByteSlicePool struct {
	p sync.Pool
}

func (b *ByteSlicePool) Get() []byte {
	return b.p.Get().([]byte)
}

func (b *ByteSlicePool) Put(bytes []byte) {
	b.p.Put(bytes)
}
