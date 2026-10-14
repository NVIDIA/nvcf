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
	Anchor,
	Divider,
	getCodeSnippetLanguage,
	TableBody,
	TableDataCell,
	TableHead,
	TableHeaderCell,
	TableRoot,
	TableRow,
	Text,
} from "@nvidia/foundations-react-core";
import { memo } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { CodeSnippet } from "~/components/CodeSnippet";

/** The parts of a markdown or HTML syntax tree node this file reads. */
interface TreeNode {
	type: string;
	tagName?: string;
	value?: string;
	properties?: { className?: unknown };
	children?: TreeNode[];
}

/**
 * Shows raw HTML in a reply as the text it is. A model's output is untrusted,
 * and react-markdown would otherwise drop it silently.
 */
function htmlAsText() {
	const walk = (node: TreeNode) => {
		if (node.type === "html") node.type = "text";
		node.children?.forEach(walk);
	};
	return walk;
}

function textOf(node: TreeNode): string {
	if (node.type === "text") return node.value ?? "";
	return (node.children ?? []).map(textOf).join("");
}

/** A fenced block's code and language, from its `<pre><code class="language-x">`. */
function codeBlock(pre: TreeNode | undefined) {
	const code = pre?.children?.find((child) => child.tagName === "code");
	const className = code?.properties?.className;
	const language = (Array.isArray(className) ? className : [])
		.map(String)
		.find((name) => name.startsWith("language-"))
		?.slice("language-".length);
	return {
		text: (code ? textOf(code) : "").replace(/\n$/, ""),
		language: getCodeSnippetLanguage(language ?? "text", "text"),
	};
}

const components: Components = {
	p: ({ children }) => (
		<Text asChild kind="body/regular/md">
			<p>{children}</p>
		</Text>
	),
	h1: ({ children }) => (
		<Text asChild kind="title/sm">
			<h2>{children}</h2>
		</Text>
	),
	h2: ({ children }) => (
		<Text asChild kind="title/xs">
			<h3>{children}</h3>
		</Text>
	),
	h3: ({ children }) => (
		<Text asChild kind="body/bold/md">
			<h4>{children}</h4>
		</Text>
	),
	h4: ({ children }) => (
		<Text asChild kind="body/bold/md">
			<h5>{children}</h5>
		</Text>
	),
	h5: ({ children }) => (
		<Text asChild kind="body/bold/md">
			<h6>{children}</h6>
		</Text>
	),
	h6: ({ children }) => (
		<Text asChild kind="body/bold/md">
			<h6>{children}</h6>
		</Text>
	),
	ul: ({ children }) => (
		<ul className="flex list-disc flex-col gap-1 pl-6">{children}</ul>
	),
	ol: ({ children }) => (
		<ol className="flex list-decimal flex-col gap-1 pl-6">{children}</ol>
	),
	li: ({ children }) => (
		<Text asChild kind="body/regular/md">
			<li className="[&>ol]:mt-1 [&>ul]:mt-1">{children}</li>
		</Text>
	),
	blockquote: ({ children }) => (
		<blockquote className="flex flex-col gap-3 border-base border-l-2 pl-3 text-secondary">
			{children}
		</blockquote>
	),
	hr: () => <Divider />,
	// Inline code; fenced blocks are rendered whole by `pre` below.
	code: ({ children }) => (
		<Text asChild kind="mono/sm">
			<code className="rounded-sm bg-background-subtle px-1">{children}</code>
		</Text>
	),
	pre: ({ node }) => {
		const { text, language } = codeBlock(node as TreeNode | undefined);
		return <CodeSnippet language={language} value={text} />;
	},
	// Links open apart from the chat. react-markdown already drops unsafe
	// URLs such as javascript:.
	a: ({ href, children }) => (
		<Anchor
			href={href}
			kind="inline"
			rel="noopener noreferrer nofollow"
			target="_blank"
		>
			{children}
		</Anchor>
	),
	// Never load an image a model points at: the demo runs offline, and the
	// Content-Security-Policy would block it anyway. Link to it instead.
	img: ({ src, alt }) =>
		typeof src === "string" && src ? (
			<Anchor
				href={src}
				kind="inline"
				rel="noopener noreferrer nofollow"
				target="_blank"
			>
				{alt || src}
			</Anchor>
		) : null,
	table: ({ children }) => (
		<div className="w-full overflow-x-auto rounded-[var(--radius-density-xl)] border border-base">
			<TableRoot className="w-full" density="compact">
				{children}
			</TableRoot>
		</div>
	),
	thead: ({ children }) => <TableHead>{children}</TableHead>,
	tbody: ({ children }) => <TableBody>{children}</TableBody>,
	tr: ({ children }) => <TableRow>{children}</TableRow>,
	th: ({ children }) => <TableHeaderCell>{children}</TableHeaderCell>,
	td: ({ children }) => <TableDataCell>{children}</TableDataCell>,
};

/**
 * A model's reply as markdown (GitHub flavored: tables, task lists,
 * strikethrough), drawn with KUI's typography and components. Output is
 * untrusted: nothing renders as HTML, links can't run script, and no image is
 * fetched.
 */
export const Markdown = memo(function Markdown({
	children,
	className,
}: {
	children: string;
	className?: string;
}) {
	return (
		<div
			className={`flex min-w-0 flex-col gap-3 [overflow-wrap:anywhere] ${className ?? ""}`}
		>
			<ReactMarkdown
				components={components}
				remarkPlugins={[remarkGfm, htmlAsText]}
			>
				{children}
			</ReactMarkdown>
		</div>
	);
});
