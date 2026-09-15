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

package pointerutil

import (
	"os"
	"time"
)

// StringPtr returns a pointer to a string value
func StringPtr(s string) *string {
	return &s
}

// BoolPtr returns a pointer to a boolean value
func BoolPtr(b bool) *bool {
	return &b
}

// TimeDurationPtr returns a pointer to a time duration value
func TimeDurationPtr(duration string) *time.Duration {
	d, _ := time.ParseDuration(duration)

	return &d
}

// FileModePtr returns a pointer to the given os.FileMode
func FileModePtr(o os.FileMode) *os.FileMode {
	return &o
}

// Int64Ptr returns a pointer to an int64 value
func Int64Ptr(i int64) *int64 {
	return &i
}
