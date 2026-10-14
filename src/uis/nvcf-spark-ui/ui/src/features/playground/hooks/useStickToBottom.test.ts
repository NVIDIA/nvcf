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

import { act, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useStickToBottom } from "./useStickToBottom";

/** The conversation's visible height, in px. */
const VIEW = 400;

function Harness() {
	const { scrollerRef, contentRef, pin } = useStickToBottom();
	return createElement(
		"div",
		null,
		createElement(
			"div",
			{ "data-testid": "scroller", ref: scrollerRef },
			createElement("section", { ref: contentRef }),
		),
		createElement("button", { onClick: pin, type: "button" }, "Pin"),
	);
}

describe("useStickToBottom", () => {
	let resize: () => void;
	let scroller: HTMLElement;
	let top: number;
	let height: number;

	beforeEach(() => {
		vi.stubGlobal(
			"ResizeObserver",
			class {
				constructor(callback: () => void) {
					resize = callback;
				}
				observe() {}
				disconnect() {}
				unobserve() {}
			},
		);
		render(createElement(Harness));
		scroller = screen.getByTestId("scroller");
		height = 1_000;
		top = height - VIEW;
		Object.defineProperties(scroller, {
			scrollHeight: { configurable: true, get: () => height },
			clientHeight: { configurable: true, get: () => VIEW },
			scrollTop: {
				configurable: true,
				get: () => top,
				set: (value: number) => {
					top = Math.max(0, Math.min(value, height - VIEW));
				},
			},
		});
		// The conversation starts at its end, as the browser reports it.
		scroller.dispatchEvent(new Event("scroll"));
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	/** The reply streams in: the content grows, and the observer reports it. */
	function grow(by = 500) {
		height += by;
		act(() => resize());
	}

	function scrollTo(y: number) {
		top = y;
		scroller.dispatchEvent(new Event("scroll"));
	}

	function touch(type: string, clientY: number) {
		scroller.dispatchEvent(
			Object.assign(new Event(type), { touches: [{ clientY }] }),
		);
	}

	it("follows new content while the reader is at the end", () => {
		grow();
		expect(top).toBe(height - VIEW);
	});

	it("stays put once the reader wheels up, even before the page scrolls", () => {
		scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
		grow();
		expect(top).toBe(600);
	});

	it("stays released through the first, small steps of a smooth scroll up", () => {
		scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
		// 30px up: still near the end, but moving away from it.
		scrollTo(570);
		grow();
		expect(top).toBe(570);
	});

	it("keeps following when the reader wheels down", () => {
		scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 120 }));
		grow();
		expect(top).toBe(height - VIEW);
	});

	it("lets the reader page or swipe back up", () => {
		scroller.dispatchEvent(new KeyboardEvent("keydown", { key: "PageUp" }));
		grow();
		expect(top).toBe(600);

		scrollTo(height - VIEW);
		touch("touchstart", 100);
		touch("touchmove", 160);
		grow();
		expect(top).toBe(1_100);
	});

	it("releases when the scrollbar is dragged away from the end", () => {
		scroller.dispatchEvent(new Event("pointerdown"));
		scrollTo(100);
		window.dispatchEvent(new Event("pointerup"));
		grow();
		expect(top).toBe(100);
	});

	it("follows again once the reader is back at the end", () => {
		scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
		scrollTo(0);
		grow();
		expect(top).toBe(0);

		scrollTo(height - VIEW - 40);
		grow();
		expect(top).toBe(height - VIEW);
	});

	it("jumps to the end and follows again when pinned", () => {
		scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
		scrollTo(0);
		act(() => screen.getByRole("button", { name: "Pin" }).click());
		expect(top).toBe(height - VIEW);
		grow();
		expect(top).toBe(height - VIEW);
	});
});
