// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package api

import (
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

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"text leaf counts and keys do not", `{"model":"m","input":"hello"}`, estimatedTokenCountForText("m") + hello},
		{"nested strings in arrays and objects count", `{"input":"hello","questions":[{"predicate":"Is it red?"}]}`, hello + question},
		{"numbers booleans and null do not count", `{"n":12345,"b":true,"x":null}`, 0},
		{"empty string counts nothing", `{"input":""}`, 0},
		{"input_image object is one image", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + bigBase64 + `","detail":"high"}]}`, imageTokens},
		{"input_image base64 length does not matter", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,` + smallBase64 + `"}]}`, imageTokens},
		{"images array counts each element", `{"images":["` + bigBase64 + `","` + smallBase64 + `",{"mediaType":"image/png","base64":"` + smallBase64 + `"}],"state":"hello"}`, 3*imageTokens + hello},
		{"assets map counts each value", `{"assets":{"a":{"mediaType":"image/png","base64":"` + bigBase64 + `"},"b":"data:image/jpeg;base64,` + smallBase64 + `"}}`, 2 * imageTokens},
		{"data url anywhere is one image", `{"state":{"screenshot":"data:image/png;base64,` + bigBase64 + `"},"input":"hello"}`, imageTokens + hello},
		{"data url without base64 is text", `{"input":"data:text/plain,hi"}`, estimatedTokenCountForText("data:text/plain,hi")},
		{"images key that is not an array is walked as text", `{"images":"hello"}`, hello},
		{"duplicate keys are all counted", `{"input":"hello","input":"hello"}`, 2 * hello},
		{"duplicate images keys are all counted", `{"images":["a"],"images":["b","c"]}`, 3 * imageTokens},
		{"mixed text and images", `{"input":"hello","images":["x"],"questions":[{"type":"input_image","image_url":"u"},{"predicate":"Is it red?"}]}`, hello + 2*imageTokens + question},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, estimatedTokenCountForDecisionBody([]byte(tc.body), imageTokens))
		})
	}
}

func TestEstimatedTokenCountForDecisionBodyImageTokensIsConfigurable(t *testing.T) {
	t.Parallel()

	body := []byte(`{"images":["a","b"]}`)
	require.Equal(t, 0, estimatedTokenCountForDecisionBody(body, 0))
	require.Equal(t, 2*7, estimatedTokenCountForDecisionBody(body, 7))
}

func TestEstimatedTokenCountForDecisionBodyInvalidJSONFallsBackToText(t *testing.T) {
	t.Parallel()

	raw := `{"input":"hello",`
	require.Equal(t, estimatedTokenCountForText(raw), estimatedTokenCountForDecisionBody([]byte(raw), 1000))
}
