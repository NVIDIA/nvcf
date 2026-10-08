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

import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useStickToBottom } from "./useStickToBottom";

describe("useStickToBottom", () => {
	let scrollTo: ReturnType<typeof vi.spyOn>;

	beforeEach(() => {
		scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
		Object.defineProperty(document.documentElement, "scrollHeight", {
			configurable: true,
			value: 5_000,
		});
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	function scrollPageTo(y: number) {
		vi.spyOn(window, "scrollY", "get").mockReturnValue(y);
		window.dispatchEvent(new Event("scroll"));
	}

	it("follows new content while the reader is at the end", () => {
		const { rerender } = renderHook(
			({ content }) => useStickToBottom(content),
			{
				initialProps: { content: "a" },
			},
		);
		scrollTo.mockClear();

		rerender({ content: "ab" });

		expect(scrollTo).toHaveBeenCalledWith({ top: 5_000 });
	});

	it("stays put after the reader scrolls up, until they scroll back down", () => {
		const { rerender } = renderHook(
			({ content }) => useStickToBottom(content),
			{
				initialProps: { content: "a" },
			},
		);
		scrollPageTo(0);
		scrollTo.mockClear();

		rerender({ content: "ab" });
		expect(scrollTo).not.toHaveBeenCalled();

		scrollPageTo(5_000 - window.innerHeight);
		rerender({ content: "abc" });
		expect(scrollTo).toHaveBeenCalledWith({ top: 5_000 });
	});
});
