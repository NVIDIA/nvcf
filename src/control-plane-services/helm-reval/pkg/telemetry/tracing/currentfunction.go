/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package tracing

import (
	"fmt"
	"runtime"
	"strings"
)

// CurrentFunction returns the name of the current function, use 'skip' to get the name of the calling function
func CurrentFunction(skip int) (string, string) {
	// Skip controls how many stack frames to ascend:
	// 0 - This function
	// 1 - The function calling this function
	// 2 - The function calling the caller, etc.
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown", "unknown"
	}
	fn := runtime.FuncForPC(pc).Name()
	parts := strings.SplitN(fn, ".", 2)
	var trimmedFn string
	if len(parts) > 1 {
		trimmedFn = parts[1]
	} else {
		trimmedFn = "unknown"
	}

	return fmt.Sprintf("%s:%d [%s]", file, line, trimmedFn), trimmedFn
}
