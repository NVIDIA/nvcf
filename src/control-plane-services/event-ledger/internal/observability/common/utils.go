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

package common

import (
	"fmt"
	"runtime"
	"strings"
)

func CurrentFunction(skip int) (string, string) {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown", "unknown"
	}

	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return fmt.Sprintf("%s:%d [unknown]", file, line), "unknown"
	}

	parts := strings.Split(fn.Name(), "/")
	serviceObject := parts[len(parts)-1]
	receiverParts := strings.Split(serviceObject, ".")
	receiver := serviceObject
	if len(receiverParts) > 1 {
		receiver = strings.Join(receiverParts[1:], ".")
	}

	return fmt.Sprintf("%s:%d [%s]", file, line, receiver), receiver
}
