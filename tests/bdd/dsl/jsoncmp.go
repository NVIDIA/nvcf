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

package dsl

import (
	"encoding/json"
	"fmt"
)

// JSONContainsRows parses raw as a JSON array of objects and, for each
// row map, asserts that an object matching every (key, value) pair in
// the row exists in the array. Extra objects in the array are
// tolerated; ordering is not asserted. Cells run through Interpolate
// before comparison, so ${VAR} works inside table values.
func JSONContainsRows(raw string, rows []map[string]string) error {
	var objects []map[string]any
	if err := json.Unmarshal([]byte(raw), &objects); err != nil {
		return fmt.Errorf("parse json output: %w", err)
	}
	for _, row := range rows {
		if err := requireRowMatch(objects, row); err != nil {
			return err
		}
	}
	return nil
}

func requireRowMatch(objects []map[string]any, row map[string]string) error {
	for _, obj := range objects {
		if rowMatchesObject(row, obj) {
			return nil
		}
	}
	return fmt.Errorf("no json object matches row %v", row)
}

func rowMatchesObject(row map[string]string, obj map[string]any) bool {
	for k, v := range row {
		actual, ok := obj[k]
		if !ok {
			return false
		}
		if fmt.Sprint(actual) != Interpolate(v) {
			return false
		}
	}
	return true
}
