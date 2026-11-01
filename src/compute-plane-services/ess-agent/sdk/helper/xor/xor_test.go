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

package xor

import (
	"encoding/base64"
	"testing"
)

const (
	tokenB64    = "ZGE0N2JiODkzYjhkMDYxYw=="
	xorB64      = "iGiQYG9L0nIp+jRL5+Zk2w=="
	expectedB64 = "7AmkVw0p6ksamAwv19BVuA=="
)

func TestBase64XOR(t *testing.T) {
	ret, err := XORBase64(tokenB64, xorB64)
	if err != nil {
		t.Fatal(err)
	}
	if res := base64.StdEncoding.EncodeToString(ret); res != expectedB64 {
		t.Fatalf("bad: %s", res)
	}
}
