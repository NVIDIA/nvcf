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

package compressutil

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestCompressUtil_CompressDecompress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		compressionType   string
		compressionConfig CompressionConfig
		canary            byte
	}{
		{
			"GZIP default implicit",
			CompressionConfig{Type: CompressionTypeGzip},
			CompressionCanaryGzip,
		},
		{
			"GZIP default explicit",
			CompressionConfig{Type: CompressionTypeGzip, GzipCompressionLevel: gzip.DefaultCompression},
			CompressionCanaryGzip,
		},
		{
			"GZIP best speed",
			CompressionConfig{Type: CompressionTypeGzip, GzipCompressionLevel: gzip.BestSpeed},
			CompressionCanaryGzip,
		},
		{
			"GZIP best compression",
			CompressionConfig{Type: CompressionTypeGzip, GzipCompressionLevel: gzip.BestCompression},
			CompressionCanaryGzip,
		},
		{
			"Snappy",
			CompressionConfig{Type: CompressionTypeSnappy},
			CompressionCanarySnappy,
		},
		{
			"LZ4",
			CompressionConfig{Type: CompressionTypeLZ4},
			CompressionCanaryLZ4,
		},
		{
			"LZW",
			CompressionConfig{Type: CompressionTypeLZW},
			CompressionCanaryLZW,
		},
	}

	inputJSONBytes := []byte(`{"sample":"data","verification":"process"}`)

	for _, test := range tests {
		// Compress the input
		compressedJSONBytes, err := Compress(inputJSONBytes, &test.compressionConfig)
		if err != nil {
			t.Fatalf("compress error (%s): %s", test.compressionType, err)
		}
		if len(compressedJSONBytes) == 0 {
			t.Fatalf("failed to compress data in %s format", test.compressionType)
		}

		// Check the presence of the canary
		if compressedJSONBytes[0] != test.canary {
			t.Fatalf("bad (%s): compression canary: expected: %d actual: %d", test.compressionType, test.canary, compressedJSONBytes[0])
		}

		decompressedJSONBytes, wasNotCompressed, err := Decompress(compressedJSONBytes)
		if err != nil {
			t.Fatalf("decompress error (%s): %s", test.compressionType, err)
		}

		// Check if the input for decompress was not compressed in the first place
		if wasNotCompressed {
			t.Fatalf("bad (%s): expected compressed bytes", test.compressionType)
		}

		if len(decompressedJSONBytes) == 0 {
			t.Fatalf("bad (%s): expected decompressed bytes", test.compressionType)
		}

		// Compare the value after decompression
		if !bytes.Equal(inputJSONBytes, decompressedJSONBytes) {
			t.Fatalf("bad (%s): decompressed value;\nexpected: %q\nactual: %q", test.compressionType, string(inputJSONBytes), string(decompressedJSONBytes))
		}

		decompressedJSONBytes, compressionType, wasNotCompressed, err := DecompressWithCanary(compressedJSONBytes)
		if err != nil {
			t.Fatalf("decompress error (%s): %s", test.compressionType, err)
		}

		if compressionType != test.compressionConfig.Type {
			t.Fatalf("bad compressionType value;\nexpected: %q\naction: %q", test.compressionConfig.Type, compressionType)
		}
	}
}

func TestCompressUtil_InvalidConfigurations(t *testing.T) {
	t.Parallel()

	inputJSONBytes := []byte(`{"sample":"data","verification":"process"}`)

	// Test nil configuration
	if _, err := Compress(inputJSONBytes, nil); err == nil {
		t.Fatal("expected an error")
	}

	// Test invalid configuration
	if _, err := Compress(inputJSONBytes, &CompressionConfig{}); err == nil {
		t.Fatal("expected an error")
	}
}
