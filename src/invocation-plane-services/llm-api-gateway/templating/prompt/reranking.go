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
	"fmt"
	"strings"
)

const (
	// RerankSystemPrompt is the system prompt used for reranking queries
	RerankSystemPrompt = "<|im_start|>system\nJudge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be 'yes' or 'no'.<|im_end|>\n<|im_start|>user\n"

	// RerankAssistantSuffix is the suffix added to document templates
	RerankAssistantSuffix = "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"

	// RerankDocumentPrefix is the prefix for document templates
	RerankDocumentPrefix = "<Document>: "

	// RerankDefaultInstruction is the default instruction when none is provided
	RerankDefaultInstruction = "Check if the query is relevant to the document given"
)

// FormatInstruction formats a query/doc pair with the given instruction
func TemplateQuery(instruction string, query string) string {
	if instruction == "" {
		instruction = RerankDefaultInstruction
	}

	return fmt.Sprintf("%s<Instruct>: %s\n<Query>: %s\n", RerankSystemPrompt, instruction, query)
}

func TemplateDocument(document string) string {
	return fmt.Sprintf("%s%s%s", RerankDocumentPrefix, document, RerankAssistantSuffix)
}

func ExtractDocumentFromTemplate(templatedDocument string) string {
	// Safely remove prefix and suffix only if they appear at the beginning/end
	// TrimPrefix/TrimSuffix only remove from the exact beginning/end, not from middle
	templatedDocument = strings.TrimPrefix(templatedDocument, RerankDocumentPrefix)
	templatedDocument = strings.TrimSuffix(templatedDocument, RerankAssistantSuffix)

	return templatedDocument
}
