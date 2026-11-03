// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimatedTokenCountForDecisionBody(t *testing.T) {
	t.Parallel()

	const imageTokens = 1000
	hello := estimatedTokenCountForText("hello")
	question := estimatedTokenCountForText("Is it red?")
	bigBase64 := strings.Repeat("QUJD", 100_000)
	smallBase64 := "QUJD"
	inputImage := estimatedTokenCountForText("input_image")

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"text leaf counts and keys do not", `{"model":"m","input":"hello"}`, estimatedTokenCountForText("m") + hello},
		{"nested strings in arrays and objects count", `{"input":"hello","questions":[{"predicate":"Is it red?"}]}`, hello + question},
		{"numbers booleans and null do not count", `{"n":12345,"b":true,"x":null}`, 0},
		{"empty string counts nothing", `{"input":""}`, 0},
		{"input_image object is one image plus its other text", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + bigBase64 + `","detail":"high"}]}`, imageTokens + inputImage + estimatedTokenCountForText("high")},
		{"input_image base64 length does not matter", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + smallBase64 + `"}]}`, imageTokens + inputImage},
		{"input_image base64 field is the image", `{"input":[{"type":"input_image","base64":"` + bigBase64 + `"}]}`, imageTokens + inputImage},
		{"input_image image fields count one image", `{"input":[{"image_url":"u","base64":"` + bigBase64 + `","type":"input_image"}]}`, imageTokens + inputImage},
		{"images array counts each element", `{"images":["` + bigBase64 + `","` + smallBase64 + `",{"mediaType":"image/png","base64":"` + smallBase64 + `"}],"state":"hello"}`, 3*imageTokens + hello},
		{"assets map counts each value", `{"assets":{"a":{"mediaType":"image/png","base64":"` + bigBase64 + `"},"b":"data:image/jpeg;base64,` + smallBase64 + `"}}`, 2 * imageTokens},
		{"assets with several object values", `{"assets":{"a":{"x":"1"},"b":{"y":[1,2]},"c":{"z":{}},"d":"hello"},"state":"hello"}`, 4*imageTokens + hello},
		{"data url anywhere is one image", `{"state":{"screenshot":"data:image/png;base64,` + bigBase64 + `"},"input":"hello"}`, imageTokens + hello},
		{"data url without base64 is text", `{"input":"data:text/plain,hi"}`, estimatedTokenCountForText("data:text/plain,hi")},
		{"images key that is not an array is walked as text", `{"images":"hello"}`, hello},
		{"duplicate keys are all counted", `{"input":"hello","input":"hello"}`, 2 * hello},
		{"duplicate images keys are all counted", `{"images":["a"],"images":["b","c"]}`, 3 * imageTokens},
		{"mixed text and images", `{"input":"hello","images":["x"],"questions":[{"type":"input_image","image_url":"u"},{"predicate":"Is it red?"}]}`, hello + 2*imageTokens + inputImage + question},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := estimatedTokenCountForDecisionBody([]byte(tc.body), imageTokens)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestEstimatedTokenCountForDecisionBodyResistsImageBypass checks that a field
// or prefix a backend ignores cannot turn prompt text into one flat image.
func TestEstimatedTokenCountForDecisionBodyResistsImageBypass(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("Is the sky red? ", 10_000)
	textTokens := estimatedTokenCountForText(text)
	for _, imageTokens := range []int{0, 1000} {
		for _, tc := range []struct {
			name string
			body string
			min  int
		}{
			{"root input_image type", `{"model":"fn/m","type":"input_image","questions":[{"predicate":"` + text + `"}]}`, textTokens},
			{"nested input_image keeps non-image text", `{"questions":[{"type":"input_image","predicate":"` + text + `"}]}`, textTokens},
			{"data url with empty media type", `{"questions":[{"predicate":"data:;base64,` + text + `"}]}`, textTokens},
			{"data prefix with base64 marker later", `{"questions":[{"predicate":"data:` + text + `;base64,x"}]}`, textTokens},
			{"non-image data url", `{"questions":[{"predicate":"data:text/plain;base64,` + text + `"}]}`, textTokens},
			{"image media type without base64 marker", `{"questions":[{"predicate":"data:image/png,` + text + `"}]}`, textTokens},
			{"image data url prefix followed by prose", `{"questions":[{"predicate":"data:image/png;base64,` + text + `"}]}`, textTokens},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				got, err := estimatedTokenCountForDecisionBody([]byte(tc.body), imageTokens)
				require.NoError(t, err)
				require.GreaterOrEqual(t, got, tc.min)
			})
		}
	}
}

func TestIsBase64ImageDataURL(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{
		"data:image/png;base64,iVBORw0KGgo=":                     true,
		"data:image/png;base64,QUJD+/9=":                         true,
		"data:image/webp;base64,QUJD-_9":                         true,
		"data:image/png;base64,":                                 true,
		"data:image/png;base64,QUJD QUJD":                        false,
		"data:image/png;base64,QUJD\nQUJD":                       false,
		"data:image/png;base64,Is the sky red?":                  false,
		"data:image/;base64,QUJD":                                false,
		"data:;base64,QUJD":                                      false,
		"data:text/plain;base64,QUJD":                            false,
		"data:image/png,QUJD":                                    false,
		"data:image/svg+xml;base64,QUJD":                         true,
		"data:image/vnd.ms-photo;base64,QUJD":                    true,
		"data:image/x.custom-1;base64,QUJD":                      true,
		"data:image/a b;base64,QUJD":                             false,
		"data:image/png/x;base64,QUJD":                           false,
		"data:image/" + strings.Repeat("a", 65) + ";base64,QUJD": false,
		"data:image/" + strings.Repeat("a", 64) + ";base64,QUJD": true,
		"data:image/png;charset=x;base64,QUJD":                   false,
		"prefix data:image/png;base64,QUJD":                      false,
	} {
		require.Equal(t, want, isBase64ImageDataURL(text), text)
	}
}

// One megabyte of prose behind an image data URL prefix counts as text.
func TestEstimatedTokenCountForDecisionBodyImagePrefixedProseIsText(t *testing.T) {
	t.Parallel()

	prose := "data:image/png;base64," + strings.Repeat("Is the sky red? ", 1<<16)
	got, err := estimatedTokenCountForDecisionBody([]byte(`{"predicate":"`+prose+`"}`), 1000)
	require.NoError(t, err)
	require.Equal(t, estimatedTokenCountForText(prose), got)
}

// Prose placed in the media subtype counts as text, not one image.
func TestEstimatedTokenCountForDecisionBodyProseInImageSubtypeIsText(t *testing.T) {
	t.Parallel()

	prose := "data:image/" + strings.Repeat("Is the sky red? ", 1<<16) + ";base64,QUJD"
	got, err := estimatedTokenCountForDecisionBody([]byte(`{"predicate":"`+prose+`"}`), 1000)
	require.NoError(t, err)
	require.Equal(t, estimatedTokenCountForText(prose), got)
}

func TestIsBase64ImageDataURLDoesNotAllocate(t *testing.T) {
	for _, text := range []string{
		"data:image/svg+xml;base64," + strings.Repeat("QUJD", 1<<16),
		"data:image/" + strings.Repeat("Is the sky red? ", 1<<12) + ";base64,QUJD",
	} {
		allocs := testing.AllocsPerRun(10, func() { _ = isBase64ImageDataURL(text) })
		require.Zero(t, allocs)
	}
}

func TestEstimatedTokenCountForDecisionBodyImageTokensIsConfigurable(t *testing.T) {
	t.Parallel()

	body := []byte(`{"images":["a","b"]}`)
	got, err := estimatedTokenCountForDecisionBody(body, 0)
	require.NoError(t, err)
	require.Equal(t, 0, got)
	got, err = estimatedTokenCountForDecisionBody(body, 7)
	require.NoError(t, err)
	require.Equal(t, 2*7, got)
}

func TestEstimatedTokenCountForDecisionBodyInvalidJSONFallsBackToText(t *testing.T) {
	t.Parallel()

	raw := `{"input":"hello",`
	got, err := estimatedTokenCountForDecisionBody([]byte(raw), 1000)
	require.NoError(t, err)
	require.Equal(t, estimatedTokenCountForText(raw), got)
}

func nestedDecisionBody(depth int, leaf string) string {
	return strings.Repeat(`{"a":`, depth) + `"` + leaf + `"` + strings.Repeat(`}`, depth)
}

func TestEstimatedTokenCountForDecisionBodyRejectsDeepNesting(t *testing.T) {
	t.Parallel()

	_, err := estimatedTokenCountForDecisionBody([]byte(nestedDecisionBody(64, "x")), 1000)
	require.NoError(t, err, "64 levels are allowed")
	_, err = estimatedTokenCountForDecisionBody([]byte(nestedDecisionBody(65, "x")), 1000)
	require.Error(t, err)
	_, err = estimatedTokenCountForDecisionBody([]byte(`{"images":`+strings.Repeat("[", 70)+strings.Repeat("]", 70)+`}`), 1000)
	require.Error(t, err, "nesting inside an image slot counts too")
}

// A big leaf nested deep must cost a few copies of the leaf, not one per level.
func TestEstimatedTokenCountForDecisionBodyAllocationIsBounded(t *testing.T) {
	body := []byte(nestedDecisionBody(60, strings.Repeat("a", 1<<20)))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got, err := estimatedTokenCountForDecisionBody(body, 1000)
	runtime.ReadMemStats(&after)

	require.NoError(t, err)
	require.Equal(t, estimatedTokenCountForText(strings.Repeat("a", 1<<20)), got)
	allocated := after.TotalAlloc - before.TotalAlloc
	require.Less(t, allocated, uint64(16<<20), "allocated %d bytes for a 1 MiB body", allocated)
}
