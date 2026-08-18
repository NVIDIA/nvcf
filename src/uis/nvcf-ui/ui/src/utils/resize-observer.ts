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

/* v8 ignore start */

const callbacks = new Map<Element, () => void>();
const pending = new Set<Element>();
let rafId: number | null = null;

function scheduleUpdate(): void {
	if (rafId !== null) return;

	rafId = requestAnimationFrame(() => {
		for (const element of pending) {
			callbacks.get(element)?.();
		}
		pending.clear();
		rafId = null;
	});
}

const observer =
	typeof window !== "undefined" && "ResizeObserver" in window
		? new ResizeObserver((entries) => {
				for (const entry of entries) {
					pending.add(entry.target);
				}
				scheduleUpdate();
			})
		: null;

export function observe(element: Element, callback: () => void): void {
	if (!observer) return;
	callbacks.set(element, callback);
	observer.observe(element);
}

export function unobserve(element: Element): void {
	if (!observer) return;
	callbacks.delete(element);
	observer.unobserve(element);
	pending.delete(element);
}

/* v8 ignore end */
