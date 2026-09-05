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

package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

func TestEstimatedInputTokensIncludesForwardedToolPayloads(t *testing.T) {
	t.Parallel()

	description := strings.Repeat("lookup field ", 32)
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": description,
			},
		},
	}
	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello"),
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	request.Tools = &[]models.ChatTool{
		{
			Type: models.ToolTypeFunction,
			Function: models.ChatFunctionSpec{
				Name:        "lookup",
				Description: &description,
				Parameters:  &parameters,
			},
		},
	}

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedInputTokensIncludesForwardedMultimodalPayloads(t *testing.T) {
	t.Parallel()

	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: []models.ContentPart{
					models.ContentPartText("describe this"),
				},
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	(*request.Messages)[0].Content = append(
		(*request.Messages)[0].Content,
		&models.ContentPartImageURL{
			URL:    "data:image/png;base64," + strings.Repeat("a", 512),
			Detail: "high",
		},
		models.ContentPartDocument{
			Data: map[string]any{
				"title": "specification",
				"body":  strings.Repeat("document content ", 64),
			},
		},
	)

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedTokenCountForRequestUsesModelImageEquation(t *testing.T) {
	t.Parallel()

	request := &models.ChatCompletionRequest{
		Messages: &[]models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.ChatMessageContent{
					&models.ContentPartImageURL{URL: pngDataURL(t, 29, 56)},
				},
			},
		},
	}

	qwenEstimate := estimatedTokenCountForRequest("Qwen/Qwen2.5-VL-72B", request)
	unknownEstimate := estimatedTokenCountForRequest("vendor/unknown-model", request)
	if qwenEstimate != tokensPerMessage+tokensPerRole+4 {
		t.Fatalf("Qwen request estimate = %d, want %d", qwenEstimate, tokensPerMessage+tokensPerRole+4)
	}
	if unknownEstimate != tokensPerMessage+tokensPerRole+fallbackBaseTokensPerImage+3 {
		t.Fatalf(
			"unknown-model request estimate = %d, want %d",
			unknownEstimate,
			tokensPerMessage+tokensPerRole+fallbackBaseTokensPerImage+3,
		)
	}
}

func TestEstimatedTokenCountForImageUsesImageDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		image *models.ContentPartImageURL
		want  int
	}{
		{
			name: "inline data URL",
			image: &models.ContentPartImageURL{
				URL: pngDataURL(t, 29, 56),
			},
			want: fallbackBaseTokensPerImage + 3,
		},
		{
			name: "raw base64",
			image: &models.ContentPartImageURL{
				URL: base64.StdEncoding.EncodeToString(pngBytes(t, 56, 28)),
			},
			want: fallbackBaseTokensPerImage + 2,
		},
		{
			name: "WebP data URL",
			image: &models.ContentPartImageURL{
				URL: "data:image/webp;base64," + base64.StdEncoding.EncodeToString(
					extendedWebPBytes(56, 28),
				),
			},
			want: fallbackBaseTokensPerImage + 2,
		},
		{
			name: "remote URL conservative fallback",
			image: &models.ContentPartImageURL{
				URL: "https://example.com/image.png",
			},
			want: maxFallbackTokensPerImage,
		},
		{
			name: "invalid inline image conservative fallback",
			image: &models.ContentPartImageURL{
				URL: "data:image/png;base64,aW52YWxpZA==",
			},
			want: maxFallbackTokensPerImage,
		},
		{
			name: "nil image conservative fallback",
			want: maxFallbackTokensPerImage,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := estimatedTokenCountForImage("unknown-model", test.image)
			if got != test.want {
				t.Fatalf("estimated image token count = %d, want %d", got, test.want)
			}
		})
	}
}

func TestEstimatedTokenCountWithoutImageDimensionsUsesModelEquation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		model  string
		detail string
		want   int
	}{
		{
			name:  "fixed-token model",
			model: "google/gemma-4-31b-it",
			want:  280,
		},
		{
			name:   "low-detail tiled model",
			model:  "openai/gpt-4o-mini",
			detail: "low",
			want:   2833,
		},
		{
			name:   "high-detail tiled model",
			model:  "openai/gpt-4o-mini",
			detail: "high",
			want:   25501,
		},
		{
			name:  "patch-budget model",
			model: "qwen/qwen2.5-vl-72b-instruct",
			want:  16384,
		},
		{
			name:  "multiplied-patch model",
			model: "openai/gpt-4.1-mini",
			want:  6636,
		},
		{
			name:  "maximum-dimension model",
			model: "stepfun-ai/step-3.7-flash",
			want:  3164,
		},
		{
			name:  "tiled-canvas model",
			model: "meta/llama-3.2-90b-vision-instruct",
			want:  6404,
		},
		{
			name:  "unknown model",
			model: "vendor/unknown-model",
			want:  maxFallbackTokensPerImage,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := estimatedTokenCountForImage(test.model, &models.ContentPartImageURL{
				URL:    "https://example.com/image.png",
				Detail: test.detail,
			})
			if got != test.want {
				t.Fatalf("estimated image token count = %d, want %d", got, test.want)
			}
		})
	}
}

func TestImageDimensionsLimitsDecodedHeader(t *testing.T) {
	t.Parallel()

	withinLimit := jpegWithLeadingComments(t, 640, 480, maxImageHeaderBytes/2)
	width, height, ok := imageDimensions(&models.ContentPartImageURL{
		URL: base64.StdEncoding.EncodeToString(withinLimit),
	})
	if !ok || width != 640 || height != 480 {
		t.Fatalf("dimensions = (%d, %d, %t), want (640, 480, true)", width, height, ok)
	}

	beyondLimit := jpegWithLeadingComments(t, 640, 480, maxImageHeaderBytes+1024)
	if _, _, ok := imageDimensions(&models.ContentPartImageURL{
		URL: base64.StdEncoding.EncodeToString(beyondLimit),
	}); ok {
		t.Fatal("image dimensions decoded beyond the header limit")
	}
}

func TestEstimatedTokenCountForMessageContentAddsEveryImage(t *testing.T) {
	t.Parallel()

	first := models.ContentPartImageURL{URL: pngDataURL(t, 28, 28)}
	second := &models.ContentPartImageURL{URL: pngDataURL(t, 56, 56)}
	content := models.ChatMessageContent{first, second}

	want := 2*fallbackBaseTokensPerImage + 5
	got := estimatedTokenCountForMessageContent("unknown-model", content)
	if got != want {
		t.Fatalf("estimated content token count = %d, want %d", got, want)
	}
}

func TestEstimatedTokenCountForImageIgnoresBase64PayloadLength(t *testing.T) {
	t.Parallel()

	shortImage := pngBytes(t, 56, 56)
	longImage := append(bytes.Clone(shortImage), bytes.Repeat([]byte("padding"), 1024)...)
	shortContent := models.ChatMessageContent{
		&models.ContentPartImageURL{
			URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(shortImage),
		},
	}
	longContent := models.ChatMessageContent{
		&models.ContentPartImageURL{
			URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(longImage),
		},
	}

	shortEstimate := estimatedTokenCountForMessageContent("unknown-model", shortContent)
	longEstimate := estimatedTokenCountForMessageContent("unknown-model", longContent)
	if shortEstimate != longEstimate {
		t.Fatalf(
			"estimates differ for equal dimensions: short = %d, long = %d",
			shortEstimate,
			longEstimate,
		)
	}
}

func TestEstimatedTokenCountForImageDimensionsUsesModelEquation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         string
		width, height int
		detail        string
		want          int
	}{
		{
			name:  "Qwen2.5-VL patches",
			model: "nvidia/Qwen2.5_VL-72B-Instruct",
			width: 29, height: 56,
			want: 4,
		},
		{
			name:  "Qwen2-VL minimum",
			model: "Qwen/Qwen2-VL-7B",
			width: 1, height: 1,
			want: 4,
		},
		{
			name:  "Qwen3-VL merged patches",
			model: "Qwen/Qwen3-VL-32B",
			width: 33, height: 64,
			want: 4,
		},
		{
			name:  "Pixtral row separators",
			model: "mistralai/pixtral-12b",
			width: 32, height: 16,
			want: 3,
		},
		{
			name:  "Pixtral longest edge resize",
			model: "pixtral-large",
			width: 2048, height: 1024,
			want: 2080,
		},
		{
			name:  "Gemma 3 fixed tokens",
			model: "google/gemma-3-27b-it",
			width: 1920, height: 1080,
			want: 256,
		},
		{
			name:  "Claude standard patches",
			model: "claude-sonnet-4-5",
			width: 29, height: 56,
			want: 4,
		},
		{
			name:  "Claude standard cap",
			model: "claude-sonnet-4-5",
			width: 4000, height: 4000,
			want: 1568,
		},
		{
			name:  "Claude high resolution cap",
			model: "anthropic/claude-opus-4.7",
			width: 4000, height: 4000,
			want: 4784,
		},
		{
			name:  "Claude current high resolution alias",
			model: "anthropic/claude-opus-4.8",
			width: 1920, height: 1080,
			want: 2691,
		},
		{
			name:  "Claude standard longest edge resize",
			model: "anthropic/claude-sonnet-4.6",
			width: 8000, height: 100,
			want: 56,
		},
		{
			name:  "MiniMax M3 merged patches",
			model: "minimax/minimax-m3",
			width: 672, height: 672,
			want: 576,
		},
		{
			name:  "Nemotron Omni minimum patch budget",
			model: "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning",
			width: 32, height: 32,
			want: 256,
		},
		{
			name:  "Step 3.7 global view",
			model: "stepfun-ai/step-3.7-flash",
			width: 728, height: 728,
			want: 171,
		},
		{
			name:  "Step 3.7 detail crops",
			model: "stepfun-ai/step-3.7-flash",
			width: 1024, height: 1024,
			want: 504,
		},
		{
			name:  "Step 3.7 pads extreme panoramas",
			model: "stepfun-ai/step-3.7-flash",
			width: 3024, height: 1,
			want: 3164,
		},
		{
			name:  "Gemma 4 soft-token budget",
			model: "google/gemma-4-31b-it",
			width: 1920, height: 1080,
			want: 280,
		},
		{
			name:  "Nemotron Nano VL detail tiles and thumbnail",
			model: "nvidia/nemotron-nano-12b-v2-vl",
			width: 1024, height: 1024,
			want: 1280,
		},
		{
			name:  "Llama 3.2 Vision tiled canvas",
			model: "meta/llama-3.2-90b-vision-instruct",
			width: 1024, height: 1024,
			want: 6404,
		},
		{
			name:  "Llama 3.2 11B Vision alias",
			model: "meta/llama-3.2-11b-vision-instruct",
			width: 560, height: 560,
			want: 1601,
		},
		{
			name:  "GPT-4o low detail",
			model: "openai/gpt-4o",
			width: 1024, height: 1024,
			detail: "low",
			want:   85,
		},
		{
			name:  "GPT-4o high detail tiles",
			model: "openai/gpt-4o-2024-11-20",
			width: 1024, height: 1024,
			detail: "high",
			want:   765,
		},
		{
			name:  "GPT-4o Mini tile costs",
			model: "gpt-4o-mini",
			width: 1024, height: 1024,
			detail: "auto",
			want:   25501,
		},
		{
			name:  "GPT-5.1 tile costs",
			model: "gpt-5.1",
			width: 1024, height: 1024,
			detail: "high",
			want:   630,
		},
		{
			name:  "GPT-4.1 tile model",
			model: "gpt-4.1-2025-04-14",
			width: 1024, height: 1024,
			detail: "auto",
			want:   765,
		},
		{
			name:  "GPT-4.1 Mini patches",
			model: "gpt-4.1-mini",
			width: 1024, height: 1024,
			detail: "auto",
			want:   1659,
		},
		{
			name:  "GPT-4.1 Nano patches",
			model: "gpt-4.1-nano-2025-04-14",
			width: 1024, height: 1024,
			detail: "auto",
			want:   2520,
		},
		{
			name:  "unknown model fallback",
			model: "vendor/new-vision-model",
			width: 29, height: 56,
			want: fallbackBaseTokensPerImage + 3,
		},
		{
			name:  "model fragment must start the final path component",
			model: "claude-inc/custom-gpt-5.1-wrapper",
			width: 29, height: 56,
			want: fallbackBaseTokensPerImage + 3,
		},
		{
			name:  "model fragment requires a trailing boundary",
			model: "vendor/gpt-4-1106-preview",
			width: 29, height: 56,
			want: fallbackBaseTokensPerImage + 3,
		},
		{
			name:  "invalid dimensions fallback",
			model: "vendor/new-vision-model",
			width: 0, height: 56,
			want: defaultTokensPerImage,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := estimatedTokenCountForImageDimensions(
				test.model,
				test.width,
				test.height,
				test.detail,
			)
			if got != test.want {
				t.Fatalf("estimated image token count = %d, want %d", got, test.want)
			}
		})
	}
}

func TestEstimatedTokenCountForImageDimensionsCapsLargeImages(t *testing.T) {
	t.Parallel()

	got := estimatedTokenCountForImageDimensions("unknown-model", 3585, 3585, "auto")
	if got != maxFallbackTokensPerImage {
		t.Fatalf("estimated image token count = %d, want %d", got, maxFallbackTokensPerImage)
	}
}

func TestWebPDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		data   []byte
		width  int
		height int
		ok     bool
	}{
		{name: "extended", data: extendedWebPBytes(640, 480), width: 640, height: 480, ok: true},
		{name: "lossless", data: losslessWebPBytes(320, 240), width: 320, height: 240, ok: true},
		{name: "lossy", data: lossyWebPBytes(800, 600), width: 800, height: 600, ok: true},
		{name: "invalid", data: []byte("not a WebP image")},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			width, height, ok := webPDimensions(test.data)
			if width != test.width || height != test.height || ok != test.ok {
				t.Fatalf(
					"dimensions = (%d, %d, %t), want (%d, %d, %t)",
					width,
					height,
					ok,
					test.width,
					test.height,
					test.ok,
				)
			}
		})
	}
}

func TestEstimatedTokenCountForValueFallsBackWhenJSONMarshalFails(t *testing.T) {
	t.Parallel()

	got := estimatedTokenCountForValue(make(chan int))
	if got == 0 {
		t.Fatal("estimated token count = 0, want fallback estimate")
	}
}

func pngDataURL(t *testing.T, width, height int) string {
	t.Helper()
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(
		pngBytes(t, width, height),
	)
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return encoded.Bytes()
}

func jpegWithLeadingComments(
	t *testing.T,
	width, height, commentBytes int,
) []byte {
	t.Helper()

	var encoded bytes.Buffer
	if err := jpeg.Encode(
		&encoded,
		image.NewRGBA(image.Rect(0, 0, width, height)),
		nil,
	); err != nil {
		t.Fatalf("encode test JPEG: %v", err)
	}

	jpegBytes := encoded.Bytes()
	result := make([]byte, 0, len(jpegBytes)+commentBytes)
	result = append(result, jpegBytes[:2]...)
	for commentBytes > 0 {
		payloadBytes := min(commentBytes, 65533)
		segmentBytes := payloadBytes + 2
		result = append(
			result,
			0xff,
			0xfe,
			byte(segmentBytes>>8),
			byte(segmentBytes),
		)
		result = append(result, make([]byte, payloadBytes)...)
		commentBytes -= payloadBytes
	}
	return append(result, jpegBytes[2:]...)
}

func extendedWebPBytes(width, height int) []byte {
	encoded := make([]byte, 30)
	initializeWebPHeader(encoded, "VP8X")
	binary.LittleEndian.PutUint32(encoded[16:20], 10)
	width--
	height--
	encoded[24] = byte(width)
	encoded[25] = byte(width >> 8)
	encoded[26] = byte(width >> 16)
	encoded[27] = byte(height)
	encoded[28] = byte(height >> 8)
	encoded[29] = byte(height >> 16)
	return encoded
}

func losslessWebPBytes(width, height int) []byte {
	encoded := make([]byte, 25)
	initializeWebPHeader(encoded, "VP8L")
	binary.LittleEndian.PutUint32(encoded[16:20], 5)
	encoded[20] = 0x2f
	bits := uint32(width-1) | uint32(height-1)<<14
	binary.LittleEndian.PutUint32(encoded[21:25], bits)
	return encoded
}

func lossyWebPBytes(width, height int) []byte {
	encoded := make([]byte, 30)
	initializeWebPHeader(encoded, "VP8 ")
	binary.LittleEndian.PutUint32(encoded[16:20], 10)
	copy(encoded[23:26], []byte{0x9d, 0x01, 0x2a})
	binary.LittleEndian.PutUint16(encoded[26:28], uint16(width))
	binary.LittleEndian.PutUint16(encoded[28:30], uint16(height))
	return encoded
}

func initializeWebPHeader(encoded []byte, chunkType string) {
	copy(encoded[0:4], "RIFF")
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(len(encoded)-8))
	copy(encoded[8:12], "WEBP")
	copy(encoded[12:16], chunkType)
}
