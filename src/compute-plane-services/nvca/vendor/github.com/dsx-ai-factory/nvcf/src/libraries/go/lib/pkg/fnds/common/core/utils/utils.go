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

package utils

import (
	"reflect"
)

// HasZeroValues Utility function for checking structs
func HasZeroValues(data interface{}) bool {
	val := reflect.ValueOf(data)

	// Dereference pointer if needed
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return true
		}
		val = val.Elem()
	}

	// Ensure we're working with a struct
	if val.Kind() != reflect.Struct {
		return false
	}

	// Iterate over all fields
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		// Skip unexported fields (cannot call Interface() on them)
		if !field.CanInterface() {
			continue
		}

		if reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
			return true // Found a zero value
		}
	}
	return false // No zero values
}
