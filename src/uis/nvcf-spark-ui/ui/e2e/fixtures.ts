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

import AxeBuilder from "@axe-core/playwright";
import { test as base, expect, type Page } from "@playwright/test";

/** Must match `SCENARIO_STORAGE_KEY` in `src/mocks/handlers.ts`. */
const SCENARIO_STORAGE_KEY = "msw:scenario";

/**
 * `scenario` picks MSW scenarios for a test (`feature:name`, comma-separated).
 * It is written to localStorage before the page loads, and the mock worker
 * reads it at startup. Don't use `page.route()`: MSW's service worker answers
 * first.
 */
export const test = base.extend<{
	scenario: string | undefined;
	sameOriginOnly: undefined;
}>({
	scenario: [undefined, { option: true }],
	page: async ({ page, scenario }, use) => {
		if (scenario) {
			await page.addInitScript(
				([key, value]) => localStorage.setItem(key, value),
				[SCENARIO_STORAGE_KEY, scenario] as const,
			);
		}
		await use(page);
	},
	// The demo runs with no external network, so every
	// test fails if the page requests anything from another origin: a CDN
	// script, a web font, telemetry.
	sameOriginOnly: [
		async ({ page, baseURL }, use) => {
			const origin = new URL(baseURL ?? "http://localhost").origin;
			const external: string[] = [];
			page.on("request", (request) => {
				const url = new URL(request.url());
				if (url.protocol === "data:" || url.protocol === "blob:") return;
				if (url.origin !== origin) external.push(request.url());
			});
			await use(undefined);
			expect(external, "requests outside the app's origin").toEqual([]);
		},
		{ auto: true },
	],
});

export { expect };

/** KUI breakpoints in px, which aren't Tailwind's defaults. */
export const BREAKPOINTS = { sm: 576, md: 768, lg: 992, xl: 1200, "2xl": 1600 };

/** Widths on both sides of every KUI breakpoint, from a small phone up. */
export const RESIZE_WIDTHS = [
	320,
	...Object.values(BREAKPOINTS).flatMap((px) => [px - 1, px]),
	2560,
];

export async function expectNoHorizontalOverflow(page: Page) {
	const overflow = await page.evaluate(
		() =>
			document.documentElement.scrollWidth -
			document.documentElement.clientWidth,
	);
	expect(overflow, "page scrolls horizontally").toBeLessThanOrEqual(0);
}

export async function expectNoSeriousA11yViolations(page: Page) {
	const { violations } = await new AxeBuilder({ page }).analyze();
	const serious = violations
		.filter((v) => v.impact === "serious" || v.impact === "critical")
		.map((v) => ({
			id: v.id,
			impact: v.impact,
			help: v.help,
			targets: v.nodes.map((n) => n.target.join(" ")),
		}));
	expect(serious).toEqual([]);
}
