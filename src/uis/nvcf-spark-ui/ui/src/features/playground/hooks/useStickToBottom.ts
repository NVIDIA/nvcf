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

import { useCallback, useEffect, useRef } from "react";

/** How close to the end, in px, still counts as reading the latest output. */
const PINNED_THRESHOLD = 80;

/** Keys that scroll a conversation back up. */
const UP_KEYS = new Set(["ArrowUp", "PageUp", "Home"]);

/**
 * Keeps a scrolling conversation at its end while its content grows, as long
 * as the reader is there.
 * - Growth is followed from a ResizeObserver, which runs after layout and
 *   before paint, so new lines never show a frame out of place.
 * - The reader takes over the moment they start scrolling up: on the wheel,
 *   key or touch that does it, before the scroll itself. A token arriving
 *   mid-gesture can't pull them back down.
 * - Reaching the end again, by any means, resumes following.
 *
 * Attach `scrollerRef` to the element that scrolls and `contentRef` to the
 * content that grows inside it. `pin` jumps to the end and resumes following,
 * for when the reader sends a message.
 */
export function useStickToBottom<
	S extends HTMLElement = HTMLDivElement,
	C extends HTMLElement = HTMLElement,
>() {
	const scrollerRef = useRef<S>(null);
	const contentRef = useRef<C>(null);
	const pinned = useRef(true);

	const toEnd = useCallback(() => {
		const scroller = scrollerRef.current;
		if (scroller) scroller.scrollTop = scroller.scrollHeight;
	}, []);

	const pin = useCallback(() => {
		pinned.current = true;
		toEnd();
	}, [toEnd]);

	useEffect(() => {
		const scroller = scrollerRef.current;
		const content = contentRef.current;
		if (!scroller || !content) return;

		const atEnd = () =>
			scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop <=
			PINNED_THRESHOLD;
		const release = () => {
			pinned.current = false;
		};
		let dragging = false;
		let touchY: number | undefined;
		let lastTop = scroller.scrollTop;

		const onWheel = (event: WheelEvent) => {
			if (event.deltaY < 0) release();
		};
		const onKeyDown = (event: KeyboardEvent) => {
			if (UP_KEYS.has(event.key)) release();
		};
		const onTouchStart = (event: TouchEvent) => {
			touchY = event.touches[0]?.clientY;
		};
		const onTouchMove = (event: TouchEvent) => {
			const y = event.touches[0]?.clientY;
			// A finger moving down scrolls the conversation up.
			if (y !== undefined && touchY !== undefined && y > touchY) release();
			touchY = y;
		};
		const onPointerDown = () => {
			dragging = true;
		};
		const onPointerUp = () => {
			dragging = false;
		};
		const onScroll = () => {
			const movedDown = scroller.scrollTop > lastTop;
			lastTop = scroller.scrollTop;
			// Only coming down into the end resumes following: the first frames
			// of a smooth scroll up are still near the end.
			if (movedDown && atEnd()) pinned.current = true;
			// Dragging the scrollbar, or selecting text upward, leaves the end.
			else if (dragging && !atEnd()) release();
		};

		scroller.addEventListener("wheel", onWheel, { passive: true });
		scroller.addEventListener("keydown", onKeyDown);
		scroller.addEventListener("touchstart", onTouchStart, { passive: true });
		scroller.addEventListener("touchmove", onTouchMove, { passive: true });
		scroller.addEventListener("pointerdown", onPointerDown);
		window.addEventListener("pointerup", onPointerUp);
		scroller.addEventListener("scroll", onScroll, { passive: true });
		const follow =
			typeof ResizeObserver === "undefined"
				? undefined
				: new ResizeObserver(() => {
						if (pinned.current) toEnd();
					});
		follow?.observe(content);
		toEnd();

		return () => {
			follow?.disconnect();
			scroller.removeEventListener("wheel", onWheel);
			scroller.removeEventListener("keydown", onKeyDown);
			scroller.removeEventListener("touchstart", onTouchStart);
			scroller.removeEventListener("touchmove", onTouchMove);
			scroller.removeEventListener("pointerdown", onPointerDown);
			window.removeEventListener("pointerup", onPointerUp);
			scroller.removeEventListener("scroll", onScroll);
		};
	}, [toEnd]);

	return { scrollerRef, contentRef, pin };
}
