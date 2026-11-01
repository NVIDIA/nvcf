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

package connutil

import (
	"context"
	"errors"
	"sync"
)

var ErrNotInitialized = errors.New("connection has not been initialized")

// ConnectionProducer can be used as an embedded interface in the Database
// definition. It implements the methods dealing with individual database
// connections and is used in all the builtin database types.
type ConnectionProducer interface {
	Close() error
	Init(context.Context, map[string]interface{}, bool) (map[string]interface{}, error)
	Connection(context.Context) (interface{}, error)

	sync.Locker

	// DEPRECATED, will be removed in 0.12
	Initialize(context.Context, map[string]interface{}, bool) error
}
