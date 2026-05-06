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

package dbplugin

import (
	"encoding/json"
	"math"

	"google.golang.org/protobuf/types/known/structpb"
)

func mapToStruct(m map[string]interface{}) (*structpb.Struct, error) {
	// Convert any json.Number typed values to float64, since the
	// type does not have a conversion mapping defined in structpb
	for k, v := range m {
		if n, ok := v.(json.Number); ok {
			nf, err := n.Float64()
			if err != nil {
				return nil, err
			}

			m[k] = nf
		}
	}

	return structpb.NewStruct(m)
}

func structToMap(strct *structpb.Struct) map[string]interface{} {
	m := strct.AsMap()
	coerceFloatsToInt(m)
	return m
}

// coerceFloatsToInt if the floats can be coerced to an integer without losing data
func coerceFloatsToInt(m map[string]interface{}) {
	for k, v := range m {
		fVal, ok := v.(float64)
		if !ok {
			continue
		}
		if isInt(fVal) {
			m[k] = int64(fVal)
		}
	}
}

// isInt attempts to determine if the given floating point number could be represented as an integer without losing data
// This does not work for very large floats, however in this usage that's okay since we don't expect numbers that large.
func isInt(f float64) bool {
	return math.Floor(f) == f
}
