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

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

function renderMarkdown(source: string) {
	return render(<Markdown>{source}</Markdown>);
}

describe("Markdown", () => {
	it("renders emphasis, nested lists and strikethrough", () => {
		renderMarkdown(
			[
				"**Latency** is ~~simple~~ *subtle*:",
				"- first token",
				"- per token",
				"  - nested",
				"",
				"1. one",
				"2. two",
			].join("\n"),
		);

		expect(screen.getByText("Latency").tagName).toBe("STRONG");
		expect(screen.getByText("simple").tagName).toBe("DEL");
		expect(screen.getByText("subtle").tagName).toBe("EM");
		const lists = screen.getAllByRole("list");
		expect(lists).toHaveLength(3);
		expect(
			within(lists[0] as HTMLElement).getAllByRole("listitem"),
		).toHaveLength(3);
		expect(screen.getByText("two").tagName).toBe("LI");
	});

	it("shifts headings down a level, under the page's own heading", () => {
		renderMarkdown("# Title\n\n## Section\n\n###### Deepest");

		expect(
			screen.getByRole("heading", { level: 2, name: "Title" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 3, name: "Section" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 6, name: "Deepest" }),
		).toBeInTheDocument();
	});

	it("renders inline code, and fenced code as a copyable code block", async () => {
		const { container } = renderMarkdown(
			"Use `nvidia-smi`:\n\n```bash\nnvidia-smi --list-gpus\n```",
		);

		expect(screen.getByText("nvidia-smi").tagName).toBe("CODE");
		expect(container).toHaveTextContent("nvidia-smi --list-gpus");
		expect(
			await screen.findByRole("button", { name: "Copy code" }),
		).toBeInTheDocument();
	});

	it("renders GitHub-flavored tables", () => {
		renderMarkdown("| | Latency |\n| --- | --- |\n| Unit | ms |");

		const table = screen.getByRole("table");
		expect(
			within(table).getByRole("columnheader", { name: "Latency" }),
		).toBeInTheDocument();
		expect(within(table).getByRole("cell", { name: "ms" })).toBeInTheDocument();
	});

	it("opens links apart from the chat, without a referrer", () => {
		renderMarkdown("See [the docs](https://docs.example.com/a).");

		const link = screen.getByRole("link", { name: "the docs" });
		expect(link).toHaveAttribute("href", "https://docs.example.com/a");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer nofollow");
	});

	describe("untrusted output", () => {
		it("shows raw HTML as text instead of rendering it", () => {
			const { container } = renderMarkdown(
				'Inline <b>bold</b> and a block:\n\n<img src=x onerror="alert(1)">',
			);

			expect(container.querySelector("b, img")).toBeNull();
			expect(container).toHaveTextContent("Inline <b>bold</b> and a block:");
			expect(container).toHaveTextContent('<img src=x onerror="alert(1)">');
		});

		it("drops script URLs from links", () => {
			renderMarkdown("[click](javascript:alert(1))");

			expect(
				screen.getByText("click").closest("a")?.getAttribute("href") ?? "",
			).not.toMatch(/javascript/i);
		});

		it("links to an image instead of loading it", () => {
			const { container } = renderMarkdown(
				"![a chart](https://cdn.example.com/chart.png)",
			);

			expect(container.querySelector("img")).toBeNull();
			expect(screen.getByRole("link", { name: "a chart" })).toHaveAttribute(
				"href",
				"https://cdn.example.com/chart.png",
			);
		});

		it("renders nothing for an image without a source", () => {
			const { container } = renderMarkdown("![missing]()");

			expect(container.querySelector("img, a")).toBeNull();
		});
	});
});
