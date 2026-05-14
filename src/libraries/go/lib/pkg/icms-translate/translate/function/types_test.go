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

package function

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreationQueueMessageMetadata(t *testing.T) {
	metadata := common.CreationQueueMessageMetadata{RequestID: "req-1", NCAID: "nca-1"}
	msg := CreationQueueMessage{CreationQueueMessageMetadata: metadata}

	assert.Equal(t, metadata, msg.GetCreationQueueMessageMetadata())
}

func TestModelsUnmarshalJSON(t *testing.T) {
	rawModels := `[{"name":"llama","version":"1","uri":"oci://model"}]`

	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "json", data: []byte(rawModels)},
		{name: "base64", data: []byte(strconvQuote(base64.StdEncoding.EncodeToString([]byte(rawModels))))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var models Models
			require.NoError(t, json.Unmarshal(tt.data, &models))
			require.Len(t, models, 1)
			assert.Equal(t, "llama", models[0].Name)
			assert.Equal(t, "1", models[0].Version)
			assert.Equal(t, "oci://model", models[0].URI)
		})
	}

	var empty Models
	require.NoError(t, empty.UnmarshalJSON(nil))
	assert.Empty(t, empty)

	var invalid Models
	assert.Error(t, invalid.UnmarshalJSON([]byte(strconvQuote("not base64"))))
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
