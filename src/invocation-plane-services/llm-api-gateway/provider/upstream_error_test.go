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
package provider

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUpstreamErrorMessageIsBounded(t *testing.T) {
	t.Parallel()

	// A multi-byte rune straddles the cap, so the cut must back up to stay valid UTF-8.
	long := strings.Repeat("a", maxUpstreamErrorMessageBytes-1) + "é" + strings.Repeat("b", 1024)
	for name, body := range map[string]string{
		"plain text":   long,
		"JSON message": `{"error":{"message":"` + long + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			message := (&UpstreamError{Status: http.StatusBadGateway, Body: []byte(body)}).Message()
			if len(message) > maxUpstreamErrorMessageBytes {
				t.Fatalf("message length = %d, want at most %d", len(message), maxUpstreamErrorMessageBytes)
			}
			if !utf8.ValidString(message) {
				t.Fatal("message is not valid UTF-8")
			}
			if !strings.HasPrefix(long, message) || len(message) < maxUpstreamErrorMessageBytes-utf8.UTFMax {
				t.Fatalf("message is not a prefix of the upstream text near the cap (length %d)", len(message))
			}
		})
	}

	short := (&UpstreamError{Status: http.StatusBadGateway, Body: []byte("upstream connect error")}).Message()
	if short != "upstream connect error" {
		t.Fatalf("short message = %q", short)
	}
}
