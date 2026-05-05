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

package prompt

import (
	"testing"

	"github.com/nvidia-lpu/minijinja"
	"github.com/nvidia-lpu/parsec/orderedmap"
	"github.com/stretchr/testify/require"
)

func TestOrderedMapEncoding(t *testing.T) {
	t.Run("fails without wrapper", func(t *testing.T) {
		// Create an OrderedMap (simulating parsec JSON schema output)
		om := orderedmap.New()
		om.Set("type", "object")
		om.Set("properties", map[string]any{"name": "string"})

		// Create a simple Jinja template
		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate("test", "{{ schema.type }}")
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		// Try to render with unwrapped OrderedMap - this should fail
		data := map[string]any{
			"schema": om,
		}

		result, err := template.Render(data)
		if err != nil {
			t.Logf("Error (expected): %v", err)
			require.Contains(
				t,
				err.Error(),
				"unsupported type",
				"should get OrderedMap encoding error",
			)
		} else {
			t.Logf("Rendered result: %s", result)
			t.Fatal("Expected error but rendering succeeded")
		}
	})

	t.Run("tool parameters with OrderedMap", func(t *testing.T) {
		// Simulate tool parameters as returned by parsec.PrepareJSONSchema
		// which returns OrderedMaps to preserve key ordering in JSON schemas
		toolParams := orderedmap.New()
		toolParams.Set("type", "object")
		toolParams.Set("required", []any{"location", "unit"})

		properties := orderedmap.New()
		properties.Set("location", map[string]any{"type": "string"})
		properties.Set("unit", map[string]any{"type": "string"})
		toolParams.Set("properties", properties)

		// Wrap the parameters (this is what jinja.go now does)
		wrappedParams := wrapOrderedMapsRecursive(toolParams)

		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate(
			"test",
			"{{ tool.type }} {{ tool.properties.location.type }}",
		)
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		data := map[string]any{
			"tool": wrappedParams,
		}

		result, err := template.Render(data)
		require.NoError(t, err)
		require.Equal(t, "object string", result)
	})

	t.Run("deeply nested OrderedMap in tool parameters", func(t *testing.T) {
		// Simulate a complex nested JSON schema with multiple levels
		level3 := orderedmap.New()
		level3.Set("type", "string")

		level2 := orderedmap.New()
		level2.Set("field", level3)

		level1 := orderedmap.New()
		level1.Set("nested", level2)

		toolParams := orderedmap.New()
		toolParams.Set("type", "object")
		toolParams.Set("properties", level1)

		// Wrap the parameters
		wrappedParams := wrapOrderedMapsRecursive(toolParams)

		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate(
			"test",
			"{{ params.properties.nested.field.type }}",
		)
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		data := map[string]any{
			"params": wrappedParams,
		}

		result, err := template.Render(data)
		require.NoError(t, err)
		require.Equal(t, "string", result)
	})

	t.Run("preserves nil maps", func(t *testing.T) {
		// Verify that nil maps stay nil (don't become empty maps)
		var (
			nilMap  map[string]any
			wrapped = wrapOrderedMapsRecursive(nilMap)
		)
		require.Nil(t, wrapped, "nil map should remain nil")

		// Test in template context - nil should render as empty/null
		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate("test", "{% if params %}has params{% else %}no params{% endif %}")
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		data := map[string]any{
			"params": wrapped,
		}

		result, err := template.Render(data)
		require.NoError(t, err)
		require.Equal(t, "no params", result, "nil should be falsy in template")
	})

	t.Run("preserves nil slices", func(t *testing.T) {
		// Verify that nil slices stay nil (don't become empty slices)
		var (
			nilSlice []any
			wrapped  = wrapOrderedMapsRecursive(nilSlice)
		)
		require.Nil(t, wrapped, "nil slice should remain nil")

		// Test in template context
		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate("test", "{% if items %}has items{% else %}no items{% endif %}")
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		data := map[string]any{
			"items": wrapped,
		}

		result, err := template.Render(data)
		require.NoError(t, err)
		require.Equal(t, "no items", result, "nil should be falsy in template")
	})

	t.Run("nil tool parameters remain nil", func(t *testing.T) {
		// Simulate a tool with nil parameters (no parameters specified)
		var (
			nilParams map[string]any
			wrapped   = wrapOrderedMapsRecursive(nilParams)
		)
		require.Nil(t, wrapped, "nil parameters should remain nil")

		env := minijinja.NewEnvironment()
		defer env.Close()

		err := env.AddTemplate(
			"test",
			"{% if tool.parameters %}{{ tool.parameters.type }}{% else %}none{% endif %}",
		)
		require.NoError(t, err)

		template, ok := env.Template("test")
		require.True(t, ok)

		data := map[string]any{
			"tool": map[string]any{
				"name":       "test_tool",
				"parameters": wrapped,
			},
		}

		result, err := template.Render(data)
		require.NoError(t, err)
		require.Equal(t, "none", result)
	})
}
