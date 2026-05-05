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

package clients

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetUAMEvaluationURL_Format(t *testing.T) {
	url := GetUAMEvaluationURL("my-namespace", "pkg.policy")
	assert.Equal(t, "/v1/namespaces/my-namespace/evaluations/pkg.policy", url)
}

func TestGetUAMEvaluationURL_EmptyNamespace(t *testing.T) {
	url := GetUAMEvaluationURL("", "pkg.policy")
	assert.Equal(t, "/v1/namespaces//evaluations/pkg.policy", url)
}

func TestGetUAMEvaluationURL_EmptyPolicyFQDN(t *testing.T) {
	url := GetUAMEvaluationURL("my-namespace", "")
	assert.Equal(t, "/v1/namespaces/my-namespace/evaluations/", url)
}

func TestGetUAMEvaluationURL_BothEmpty(t *testing.T) {
	url := GetUAMEvaluationURL("", "")
	assert.Equal(t, "/v1/namespaces//evaluations/", url)
}
