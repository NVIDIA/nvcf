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

package models

import (
	"encoding/json"
	"testing"
)

func TestModelsHelpers(t *testing.T) {
	t.Run("FormatTime", func(t *testing.T) {
		ts := int64(1234567890)
		formatted := FormatTime(ts)
		expected := "2009-02-13T23:31:30Z"
		if formatted != expected {
			t.Errorf("expected %s, got %s", expected, formatted)
		}
	})

	t.Run("FormatTime Zero", func(t *testing.T) {
		formatted := FormatTime(0)
		if formatted != "" {
			t.Errorf("expected empty string for zero timestamp, got %s", formatted)
		}
	})
}

func TestJWTClaimsUnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name        string
		jsonData    string
		expectedAud []string
		expectError bool
	}{
		{
			name:        "aud as string",
			jsonData:    `{"iat":1234567890,"exp":1234567900,"scopes":["read"],"aud":"single-audience","sub":"user@example.com"}`,
			expectedAud: []string{"single-audience"},
			expectError: false,
		},
		{
			name:        "aud as array",
			jsonData:    `{"iat":1234567890,"exp":1234567900,"scopes":["read","write"],"aud":["aud1","aud2"],"sub":"user@example.com"}`,
			expectedAud: []string{"aud1", "aud2"},
			expectError: false,
		},
		{
			name:        "aud as empty array",
			jsonData:    `{"iat":1234567890,"exp":1234567900,"scopes":["read"],"aud":[],"sub":"user@example.com"}`,
			expectedAud: []string{},
			expectError: false,
		},
		{
			name:        "missing aud field",
			jsonData:    `{"iat":1234567890,"exp":1234567900,"scopes":["read"],"sub":"user@example.com"}`,
			expectedAud: nil,
			expectError: false,
		},
		{
			name:        "aud as number (invalid)",
			jsonData:    `{"iat":1234567890,"exp":1234567900,"scopes":["read"],"aud":123,"sub":"user@example.com"}`,
			expectedAud: nil,
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var claims JWTClaims
			err := json.Unmarshal([]byte(tc.jsonData), &claims)

			if tc.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Compare Aud slices
			if len(claims.Aud) != len(tc.expectedAud) {
				t.Errorf("expected aud length %d, got %d", len(tc.expectedAud), len(claims.Aud))
				return
			}

			for i, aud := range tc.expectedAud {
				if claims.Aud[i] != aud {
					t.Errorf("expected aud[%d] = %s, got %s", i, aud, claims.Aud[i])
				}
			}

			// Verify other fields are unmarshaled correctly
			if claims.IAT != 1234567890 {
				t.Errorf("expected iat 1234567890, got %d", claims.IAT)
			}
			if claims.EXP != 1234567900 {
				t.Errorf("expected exp 1234567900, got %d", claims.EXP)
			}
		})
	}
}
