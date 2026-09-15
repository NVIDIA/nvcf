/**
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import {
	CodeSnippetActions,
	CodeSnippetCode,
	type CodeSnippetCodeProps,
	type CodeSnippetLanguage,
	CodeSnippetRoot,
	type CodeSnippetRootProps,
} from "@nvidia/foundations-react-core";
import type { ReactNode } from "react";
import { CopyButton } from "./CopyButton";

interface CodeSnippetProps {
	value: CodeSnippetCodeProps["value"];
	language?: CodeSnippetLanguage;
	kind?: CodeSnippetRootProps["kind"];
	slotActions?: ReactNode;
	className?: string;
}

/**
 * This composes KUI's `CodeSnippet` primitives
 * so we can supply our own {@link CopyButton}. KUI's built-in
 * `CodeSnippetCopyButton` calls `navigator.clipboard.writeText` directly, which
 * is `undefined` in insecure (HTTP) contexts; our copy button falls back to
 * `execCommand` so copy works regardless of the serving scheme.
 */
export function CodeSnippet({
	value,
	language,
	kind = "block",
	slotActions,
	className,
}: CodeSnippetProps) {
	return (
		<CodeSnippetRoot className={className} kind={kind}>
			{kind === "block" ? (
				<CodeSnippetActions>
					{slotActions}
					<CopyButton ariaLabel="Copy code" value={value} />
				</CodeSnippetActions>
			) : null}
			<CodeSnippetCode language={language} value={value} />
		</CodeSnippetRoot>
	);
}
