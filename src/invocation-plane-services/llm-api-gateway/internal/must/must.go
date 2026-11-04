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

package must

import (
	"fmt"
)

// Get is a helper that panics if err is not nil, or otherwise returns x.
func Get[T any](x T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("must.Get[%T] received an error: %v", x, err))
	}
	return x
}

// Do is a helper that panics if err is not nil.
func Do(err error) {
	if err != nil {
		panic(fmt.Sprintf("must.Do received an error: %v", err))
	}
}

// True is a helper that panics if ok is false, or otherwise returns x.
func True[T any](x T, ok bool) T {
	if !ok {
		panic(fmt.Sprintf("must.True[%T] received %v", x, ok))
	}
	return x
}

// False is a helper that panics if ok is true, or otherwise returns x.
func False[T any](x T, ok bool) T {
	if ok {
		panic(fmt.Sprintf("must.False[%T] received %v", x, ok))
	}
	return x
}

// As is a helper that panics if x cannot be coerced from [From] into To.
func As[To any, From any](v From) To {
	x, ok := any(v).(To)
	if !ok {
		var zero To
		panic(fmt.Sprintf("must.As: cannot convert %T to %T", v, zero))
	}
	return x
}
