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

import { useEffect, useRef } from "react";

/** How close to the end, in px, still counts as reading the latest output. */
const PINNED_THRESHOLD = 80;

/**
 * Keeps the page scrolled to the end while `content` grows, as long as the
 * reader is already at the end. Scrolling up to reread unpins it until they
 * scroll back down.
 */
export function useStickToBottom(content: unknown) {
	const pinned = useRef(true);

	useEffect(() => {
		const onScroll = () => {
			const { scrollHeight } = document.documentElement;
			pinned.current =
				window.innerHeight + window.scrollY >= scrollHeight - PINNED_THRESHOLD;
		};
		window.addEventListener("scroll", onScroll, { passive: true });
		return () => window.removeEventListener("scroll", onScroll);
	}, []);

	// biome-ignore lint/correctness/useExhaustiveDependencies: re-runs on every change to the content.
	useEffect(() => {
		if (pinned.current) {
			window.scrollTo({ top: document.documentElement.scrollHeight });
		}
	}, [content]);
}
