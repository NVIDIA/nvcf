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

package config

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/go-viper/mapstructure/v2"
)

func numericMapToSliceHook(from reflect.Type, to reflect.Type, data any) (any, error) {
	if to.Kind() != reflect.Slice || from.Kind() != reflect.Map || from.Key().Kind() != reflect.String {
		return data, nil
	}

	value := reflect.ValueOf(data)
	out := make([]any, value.Len())
	seen := make([]bool, value.Len())
	for _, key := range value.MapKeys() {
		index, err := strconv.Atoi(key.String())
		if err != nil {
			return data, nil
		}
		if index < 0 || index >= value.Len() {
			return nil, fmt.Errorf("numeric map key %q is not a contiguous slice index", key.String())
		}
		out[index] = value.MapIndex(key).Interface()
		seen[index] = true
	}
	for index, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("numeric map is missing contiguous slice index %d", index)
		}
	}
	return out, nil
}

func defaultDecodeHook() mapstructure.DecodeHookFunc {
	return mapstructure.ComposeDecodeHookFunc(
		numericMapToSliceHook,
		mapstructure.StringToTimeDurationHookFunc(), // "1h" → time.Hour
		mapstructure.StringToSliceHookFunc(","),     // "a,b,c" → []string{"a","b","c"}
	)
}

// DecodeConfig unmarshals any input into a target struct using mapstructure.
// Uses permissive parsing - ignores extra keys in input for easier migration.
func DecodeConfig(input any, output any) error {
	config := &mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		DecodeHook:       defaultDecodeHook(),
		Result:           output,
		ErrorUnused:      false,
	}

	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}

	return decoder.Decode(input)
}

// DecodeConfigStrict is like DecodeConfig but errors on unused input keys.
// Use this for strict validation when needed.
func DecodeConfigStrict(input any, output any) error {
	config := &mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		DecodeHook:       defaultDecodeHook(),
		Result:           output,
		ErrorUnused:      true, // Error if input has extra keys
	}

	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}

	return decoder.Decode(input)
}
