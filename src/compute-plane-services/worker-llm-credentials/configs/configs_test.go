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

package configs

import (
	"reflect"
	"testing"
)

func TestConfigSupportsOptionalESSAssertionTokenPath(t *testing.T) {
	field, ok := reflect.TypeOf(Config{}).FieldByName("ESSAssertionTokenPath")
	if !ok {
		t.Fatal("Config must expose an optional ESSAssertionTokenPath field")
	}
	if got := field.Tag.Get("mapstructure"); got != "ESS_ASSERTION_TOKEN_PATH" {
		t.Fatalf("ESSAssertionTokenPath mapstructure tag = %q, want %q", got, "ESS_ASSERTION_TOKEN_PATH")
	}
}
