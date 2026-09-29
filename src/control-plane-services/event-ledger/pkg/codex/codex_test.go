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

package codex

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		msgBody  []byte
		wantErr  bool
	}{
		{
			name:     "WrapSte",
			encoding: "stage_transition_event",
			msgBody:  []byte("message"),
			wantErr:  false,
		},
		{
			name:     "BadWrap",
			encoding: "bad_wrap",
			msgBody:  []byte("message"),
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testutils.InitTestLogger(t)
			codex := NewCodex(logger)
			wrapped, err := codex.Wrap(context.Background(), tt.encoding, tt.msgBody)
			if err != nil && !tt.wantErr {
				t.Errorf("error wrapping message: %s", err.Error())
			}

			unwrapped, err := codex.Unwrap(context.Background(), wrapped)
			if err != nil && !tt.wantErr {
				t.Errorf("error decoding message: %s", err.Error())
			}
			if !tt.wantErr {
				if string(unwrapped.BinaryMessage) != string(tt.msgBody) {
					t.Errorf("decoded message body is wrong")
				}
			}

		})
	}
}
