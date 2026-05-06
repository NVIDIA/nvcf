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

package prompt

import (
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

type IncrementalTokenParser interface {
	AddToken(token uint32) error
	CurrentRole() string
	// TODO(mway): Don't leak the concept of "channel" (a Harmony-ism)
	CurrentChannel() string
	CurrentContent() string
	Messages() ([]models.ChatMessage, error)
	HasToolCalls() bool
	HasUserFunctionCalls() bool
}
