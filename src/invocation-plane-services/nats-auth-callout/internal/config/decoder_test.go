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
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/plugins/types"

	"github.com/stretchr/testify/assert"
)

func TestDecodeConfig(t *testing.T) {
	type TestStruct struct {
		StringField   string        `mapstructure:"stringField"`
		IntField      int           `mapstructure:"intField"`
		BoolField     bool          `mapstructure:"boolField"`
		DurationField time.Duration `mapstructure:"durationField"`
		SliceField    []string      `mapstructure:"sliceField"`
	}

	t.Run("should decode basic types", func(t *testing.T) {
		input := map[string]any{
			"stringField": "test",
			"intField":    123,
			"boolField":   true,
		}

		var result TestStruct
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		assert.Equal(t, "test", result.StringField)
		assert.Equal(t, 123, result.IntField)
		assert.True(t, result.BoolField)
	})

	t.Run("should handle duration string conversion", func(t *testing.T) {
		input := map[string]any{
			"durationField": "2h30m",
		}

		var result TestStruct
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		expected := 2*time.Hour + 30*time.Minute
		assert.Equal(t, expected, result.DurationField)
	})

	t.Run("should handle comma-separated string to slice conversion", func(t *testing.T) {
		input := map[string]any{
			"sliceField": "item1,item2,item3",
		}

		var result TestStruct
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		expected := []string{"item1", "item2", "item3"}
		assert.ElementsMatch(t, expected, result.SliceField)
	})

	t.Run("should handle type conversions with WeaklyTypedInput", func(t *testing.T) {
		input := map[string]any{
			"stringField": "test",
			"intField":    "456",
			"boolField":   "true",
		}

		var result TestStruct
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		assert.Equal(t, "test", result.StringField)
		assert.Equal(t, 456, result.IntField)
		assert.True(t, result.BoolField)
	})

	t.Run("should ignore extra keys (permissive parsing)", func(t *testing.T) {
		input := map[string]any{
			"stringField": "test",
			"extraKey":    "should be ignored",
			"anotherKey":  123,
		}

		var result TestStruct
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		assert.Equal(t, "test", result.StringField)
	})

	t.Run("should handle complex nested structures like Permissions", func(t *testing.T) {
		input := map[string]any{
			"publish": map[string]any{
				"allow": []any{"topic1", "topic2"},
				"deny":  []any{"secret.*"},
			},
			"subscribe": map[string]any{
				"allow": "inbox.>,stats.*", // comma-separated string to slice
				"deny":  []any{"admin.*"},
			},
			"response": map[string]any{
				"maxMsgs": 100,
				"ttl":     "5m", // duration string
			},
		}

		var result types.Permissions
		err := DecodeConfig(input, &result)
		assert.NoError(t, err)
		assert.NotNil(t, result.Publish)
		assert.ElementsMatch(t, []string{"topic1", "topic2"}, result.Publish.Allow)
		assert.ElementsMatch(t, []string{"secret.*"}, result.Publish.Deny)
		assert.NotNil(t, result.Subscribe)
		assert.ElementsMatch(t, []string{"inbox.>", "stats.*"}, result.Subscribe.Allow)
		assert.ElementsMatch(t, []string{"admin.*"}, result.Subscribe.Deny)
		assert.NotNil(t, result.Response)
		assert.Equal(t, 100, result.Response.MaxMsgs)
		expectedTTL := 5 * time.Minute
		assert.Equal(t, expectedTTL, result.Response.TTL)
	})
}

func TestDecodeConfigStrict(t *testing.T) {
	type TestStruct struct {
		StringField string `mapstructure:"stringField"`
	}

	t.Run("should error on unused keys", func(t *testing.T) {
		input := map[string]any{
			"stringField": "test",
			"extraKey":    "should cause error",
		}

		var result TestStruct
		err := DecodeConfigStrict(input, &result)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), "extraKey")
		}
	})

	t.Run("should work normally without extra keys", func(t *testing.T) {
		input := map[string]any{
			"stringField": "test",
		}

		var result TestStruct
		err := DecodeConfigStrict(input, &result)
		assert.NoError(t, err)
		assert.Equal(t, "test", result.StringField)
	})
}
