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
	expect,
	expectNoHorizontalOverflow,
	expectNoSeriousA11yViolations,
	RESIZE_WIDTHS,
	test,
} from "./fixtures";

const GLM = "GLM-5.3-UD-IQ2_M";
const glmPlayground = `/playground?model=${GLM}`;

// A reply streams for a few seconds, longer under four parallel browsers; wait
// for its stats line, which shows once the stream ends.
const REPLY_DONE = { timeout: 15_000 };

test.describe("playground", () => {
	test("streams a reply with reasoning, answer and stats", async ({ page }) => {
		await page.goto(glmPlayground);
		await expect(
			page.getByRole("heading", { level: 1, name: GLM }),
		).toBeVisible();

		await page.getByRole("button", { name: "Send" }).click();

		await expect(
			page.getByText(/^Latency is how long one request takes/),
		).toBeVisible();
		await expect(page.getByText(/\d+ tokens · first token/)).toBeVisible(
			REPLY_DONE,
		);
		// The reply's markdown renders as lists and a table, not as syntax.
		const conversation = page.getByRole("region", { name: "Conversation" });
		await expect(
			conversation
				.getByRole("listitem")
				.filter({ hasText: "Time to first token" }),
		).toBeVisible();
		await expect(conversation.getByRole("table")).toBeVisible();
		await expect(conversation).not.toContainText("**");
		await page.getByText(/^Thought for/).click();
		await expect(page.getByText(/contrasted for model serving/)).toBeVisible();
		// Phones keep the message box above the browser chrome.
		await expect(
			page.getByRole("textbox", { name: "Message" }),
		).toBeInViewport();
	});

	test("holds a multi-turn conversation", async ({ page }) => {
		await page.goto(glmPlayground);
		await page.getByRole("button", { name: "Send" }).click();
		await expect(page.getByText(/\d+ tokens · first token/)).toBeVisible(
			REPLY_DONE,
		);

		const box = page.getByRole("textbox", { name: "Message" });
		await box.fill("Thanks");
		await box.press("Enter");

		await expect(page.getByText("Thanks", { exact: true })).toBeVisible();
		await expect(page.getByText(/\d+ tokens · first token/)).toHaveCount(
			2,
			REPLY_DONE,
		);
	});

	test("survives a refresh on its deep link", async ({ page }) => {
		await page.goto(
			`/playground?model=${encodeURIComponent("qwen/qwen3.8-4b")}`,
		);
		await expect(
			page.getByRole("heading", { level: 1, name: "qwen/qwen3.8-4b" }),
		).toBeVisible();

		await page.reload();

		await expect(
			page.getByRole("heading", { level: 1, name: "qwen/qwen3.8-4b" }),
		).toBeVisible();
		await expect(page.getByRole("button", { name: "Send" })).toBeEnabled();
	});

	test("blocks chat with an unhealthy model", async ({ page }) => {
		await page.goto(
			`/playground?model=${encodeURIComponent("deepseek-ai/deepseek-v4-flash")}`,
		);

		await expect(page.getByText(/This endpoint is unhealthy/)).toBeVisible();
		await expect(page.getByRole("textbox", { name: "Message" })).toBeDisabled();
		await page.getByRole("link", { name: "View in registry" }).click();
		await expect(page).toHaveURL(
			/\/registry\?model=deepseek-ai%2Fdeepseek-v4-flash/,
		);
	});

	test("asks for a model when opened without one", async ({ page }) => {
		await page.goto("/playground");

		await expect(
			page.getByRole("heading", { name: "No model selected" }),
		).toBeVisible();
	});

	test("navigates back to the registry", async ({ page }) => {
		await page.goto(glmPlayground);

		await page.getByRole("link", { name: "NVCF Gateway" }).click();

		await expect(page).toHaveURL(/\/registry$/);
	});

	test("fits the viewport and passes axe, before and after a reply", async ({
		page,
	}) => {
		await page.goto(glmPlayground);
		await expect(page.getByRole("button", { name: "Send" })).toBeVisible();
		await expectNoHorizontalOverflow(page);
		await expectNoSeriousA11yViolations(page);

		await page.getByRole("button", { name: "Send" }).click();
		await expect(page.getByText(/\d+ tokens · first token/)).toBeVisible(
			REPLY_DONE,
		);
		await expectNoHorizontalOverflow(page);
		await expectNoSeriousA11yViolations(page);
	});

	test.describe("with a slow reply", () => {
		test.use({ scenario: "playground:slow" });

		test("stops it partway", async ({ page }) => {
			await page.goto(glmPlayground);
			await page.getByRole("button", { name: "Send" }).click();
			await expect(page.getByText(/^Thinking…/)).toBeVisible();

			await page.getByRole("button", { name: "Stop" }).click();

			await expect(page.getByText(/^Stopped/)).toBeVisible();
			await expect(page.getByRole("button", { name: "Send" })).toBeVisible();
		});
	});

	for (const [scenario, message] of [
		["playground:unauthorized", "The gateway rejected this UI's key."],
		["playground:stack-down", "Gateway unreachable."],
		["playground:busy", "The model is busy."],
		["playground:cut-off", "The response was cut off."],
	] as const) {
		test.describe(`with ${scenario}`, () => {
			test.use({ scenario });

			test("explains the failure and offers a retry", async ({ page }) => {
				await page.goto(glmPlayground);
				await page.getByRole("button", { name: "Send" }).click();

				await expect(page.getByText(message, { exact: false })).toBeVisible();
				await expect(
					page.getByRole("button", { name: "Try again" }),
				).toBeVisible();
			});
		});
	}

	test.describe("with the routing stack down", () => {
		test.use({ scenario: "registry:stack-down" });

		test("says the gateway is unreachable", async ({ page }) => {
			await page.goto(glmPlayground);

			await expect(
				page.getByRole("heading", { name: "Gateway unreachable" }),
			).toBeVisible();
		});
	});

	test("adapts across every breakpoint without reloading", async ({
		page,
	}, testInfo) => {
		test.skip(
			testInfo.project.name !== "desktop",
			"walks every width itself; once is enough",
		);
		await page.goto(glmPlayground);
		await page.getByRole("button", { name: "Send" }).click();
		await expect(page.getByText(/\d+ tokens · first token/)).toBeVisible(
			REPLY_DONE,
		);
		const conversation = page.getByRole("region", { name: "Conversation" });
		const box = page.getByRole("textbox", { name: "Message" });

		for (const width of RESIZE_WIDTHS) {
			await page.setViewportSize({ width, height: 800 });
			await expectNoHorizontalOverflow(page);
			await expect(box, `message box at ${width}px`).toBeInViewport();
			const column = await conversation.boundingBox();
			if (!column) throw new Error(`no layout at ${width}px`);
			// Focused chat: an 800px column, centered once there is room.
			expect(column.width).toBeLessThanOrEqual(800);
			if (width >= 1200) {
				const left = column.x;
				const right = width - (column.x + column.width);
				expect(Math.abs(left - right)).toBeLessThan(24);
			}
		}
	});
});
