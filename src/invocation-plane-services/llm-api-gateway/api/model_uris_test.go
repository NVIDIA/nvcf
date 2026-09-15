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
	"testing"
)

func TestModelURIMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		uri  string
		want bool
	}{
		{uri: "/v1/embeddings", want: true},
		{uri: "v1/embeddings", want: true},
		{uri: "/v1/embeddings/", want: true},
		{uri: "  /v1/embeddings  ", want: true},
		{uri: "/V1/Embeddings", want: true},
		{uri: "V1/EMBEDDINGS//", want: true},
		{uri: "/v1/chat/completions", want: false},
		{uri: "/v1/embedding", want: false},
		{uri: "", want: false},
		{uri: "/", want: false},
	}

	for _, tc := range tests {
		if got := modelURIMatches(tc.uri, embeddingsEndpointPath); got != tc.want {
			t.Errorf("modelURIMatches(%q, %q) = %v, want %v", tc.uri, embeddingsEndpointPath, got, tc.want)
		}
	}
}
