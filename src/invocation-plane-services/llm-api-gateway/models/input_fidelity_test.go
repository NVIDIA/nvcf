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

package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestChatInputAudioRoundTrip(t *testing.T) {
	for _, format := range []string{"wav", "mp3"} {
		t.Run(format, func(t *testing.T) {
			body := `[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"` + format + `"}}]`
			var content ChatMessageContent
			if err := json.Unmarshal([]byte(body), &content); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != body {
				t.Fatalf("got %s, want %s", got, body)
			}
		})
	}
}

func TestChatInputAudioKeepsPartsMissingDataOrFormatRaw(t *testing.T) {
	for _, member := range []string{``, `,"input_audio":null`, `,"input_audio":{}`, `,"input_audio":{"data":"UklGRg=="}`, `,"input_audio":{"format":"wav"}`, `,"input_audio":{"data":"","format":"wav"}`, `,"input_audio":{"data":"UklGRg==","format":""}`} {
		t.Run(member, func(t *testing.T) {
			part := `{"type":"input_audio"` + member + `}`
			var content ChatMessageContent
			if err := json.Unmarshal([]byte(`[`+part+`]`), &content); err != nil {
				t.Fatal(err)
			}
			if raw, ok := content[0].(ContentPartRaw); !ok || string(raw) != part {
				t.Fatalf("part = %#v, want raw %s", content[0], part)
			}
		})
	}
}

func TestEmbeddingInputPreservesTextAndTokenValues(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{`"hello"`, `["hello"]`},
		{`["hello","world"]`, `["hello","world"]`},
		{`[0,123,456]`, `[[0,123,456]]`},
		{`[[0,123],[456]]`, `[[0,123],[456]]`},
		// Other inputs stay raw for the backend to validate.
		{`1`, `[1]`},
		{`true`, `[true]`},
		{`{"type":"image_url"}`, `[{"type":"image_url"}]`},
		{`[1.5]`, `[[1.5]]`},
		{`[-1]`, `[[-1]]`},
		{`[4294967296]`, `[[4294967296]]`},
		{`[1,null]`, `[[1,null]]`},
		{`[[1,null]]`, `[[1,null]]`},
		{`[1,"hello"]`, `[[1,"hello"]]`},
		{`[[1],"hello",{}]`, `[[1],"hello",{}]`},
		{`[1,[2]]`, `[[1,[2]]]`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			var input EmbeddingInputField
			if err := json.Unmarshal([]byte(tt.input), &input); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			var wantValue, gotValue any
			if err := json.Unmarshal([]byte(tt.want), &wantValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got, &gotValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wantValue, gotValue) {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}
