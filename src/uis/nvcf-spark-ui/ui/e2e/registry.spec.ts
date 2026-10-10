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
	BREAKPOINTS,
	expect,
	expectNoHorizontalOverflow,
	expectNoSeriousA11yViolations,
	RESIZE_WIDTHS,
	test,
} from "./fixtures";

const LLAMA = "meta/llama-3.1-8b-instruct";
const NEMOTRON = "nvidia/nemotron-5-super-49b";

test.describe("endpoint registry", () => {
	test("finds an endpoint and shows how to call it", async ({ page }) => {
		await page.goto("/");

		await expect(page).toHaveURL(/\/registry$/);
		await expect(page.getByRole("table")).toBeVisible();
		await page.getByRole("link", { name: LLAMA }).click();

		await expect(page).toHaveURL(/model=meta%2Fllama-3\.1-8b-instruct/);
		const panel = page.getByRole("region", { name: LLAMA });
		// On phones the panel sits below the table; selecting scrolls it in.
		await expect(panel.getByRole("heading", { name: LLAMA })).toBeInViewport();
		await expect(panel.getByText("Healthy")).toBeVisible();
		await expect(panel).toContainText(
			'curl "https://llm-gateway.example.com/v1/chat/completions"',
		);
		await expect(panel).toContainText(`"model": "${LLAMA}"`);
	});

	test("copies the curl command", async ({ page, context }) => {
		await context.grantPermissions(["clipboard-read", "clipboard-write"]);
		await page.goto(`/registry?model=${encodeURIComponent(LLAMA)}`);

		await page
			.getByRole("region", { name: LLAMA })
			.getByRole("button", { name: "Copy code" })
			.click();

		await expect
			.poll(() => page.evaluate(() => navigator.clipboard.readText()))
			.toContain(`curl "https://llm-gateway.example.com/v1/chat/completions"`);
	});

	test("opens the playground for an endpoint in a new tab", async ({
		page,
		context,
	}) => {
		await page.goto(`/registry?model=${encodeURIComponent(LLAMA)}`);

		const [playground] = await Promise.all([
			context.waitForEvent("page"),
			page.getByRole("link", { name: "Try in playground" }).click(),
		]);

		await expect(playground).toHaveURL(
			/\/playground\?model=meta%2Fllama-3\.1-8b-instruct$/,
		);
		await expect(
			playground.getByRole("heading", { level: 1, name: LLAMA }),
		).toBeVisible();
	});

	test("keeps an unhealthy endpoint's selection across a deep link and a refresh", async ({
		page,
	}) => {
		await page.goto(`/registry?model=${encodeURIComponent(NEMOTRON)}`);
		const panel = page.getByRole("region", { name: NEMOTRON });
		await expect(panel.getByText("Unhealthy")).toBeVisible();
		await expect(
			panel.getByRole("button", { name: "Try in playground" }),
		).toBeDisabled();

		await page.reload();

		await expect(panel.getByText("Unhealthy")).toBeVisible();
	});

	test("navigates from the app bar", async ({ page }, testInfo) => {
		await page.goto(`/playground?model=${encodeURIComponent(LLAMA)}`);
		await page.getByRole("link", { name: "NVCF Gateway" }).click();
		await expect(page).toHaveURL(/\/registry$/);

		const nav = page.getByRole("tab", { name: "Endpoint registry" });
		if (testInfo.project.name === "mobile") {
			// Below sm the nav collapses into the expander's drawer.
			await expect(nav).toBeHidden();
			await page.goto(`/playground?model=${encodeURIComponent(LLAMA)}`);
			await page.getByRole("button", { name: "Open navigation" }).click();
			await page
				.getByRole("dialog", { name: "Navigation" })
				.getByRole("link", { name: "Endpoint registry" })
				.click();
			await expect(page).toHaveURL(/\/registry$/);
		} else {
			await page.goto(`/playground?model=${encodeURIComponent(LLAMA)}`);
			await nav.click();
			await expect(page).toHaveURL(/\/registry$/);
		}
	});

	test("fits the viewport and passes axe", async ({ page }) => {
		await page.goto("/registry");
		await expect(page.getByRole("table")).toBeVisible();

		await expectNoHorizontalOverflow(page);
		await expectNoSeriousA11yViolations(page);
	});

	test.describe("while the registry loads", () => {
		test.use({ scenario: "registry:loading" });

		test("shows the heading and placeholders", async ({ page }) => {
			await page.goto("/registry");

			await expect(
				page.getByRole("heading", { name: "Endpoint registry" }),
			).toBeVisible();
			await expect(page.getByText("Loading endpoints")).toBeAttached();
			await expect(page.getByText("Connecting…").first()).toBeAttached();
		});
	});

	test.describe("with nothing registered", () => {
		test.use({ scenario: "registry:empty" });

		test("explains the empty registry", async ({ page }) => {
			await page.goto("/registry");

			await expect(
				page.getByRole("heading", { name: "Nothing is registered yet" }),
			).toBeVisible();
			await expect(page.getByRole("table")).toHaveCount(0);
			await expectNoHorizontalOverflow(page);
			await expectNoSeriousA11yViolations(page);
		});
	});

	test.describe("with nothing serving Grafana behind the ingress", () => {
		test.use({ scenario: "registry:grafana-down" });

		test("says Grafana is unavailable instead of Page not found", async ({
			page,
			context,
		}) => {
			await page.goto(`/registry?model=${encodeURIComponent(LLAMA)}`);

			const [grafana] = await Promise.all([
				context.waitForEvent("page"),
				page
					.getByRole("region", { name: LLAMA })
					.getByRole("link", { name: "Metrics in Grafana" })
					.click(),
			]);

			await expect(grafana).toHaveURL(
				/\/grafana\/d\/llm-demo\?var-model=meta%2Fllama-3\.1-8b-instruct$/,
			);
			await expect(
				grafana.getByRole("heading", { name: "Grafana unavailable" }),
			).toBeVisible();
			await expect(grafana.getByText("Page not found")).toHaveCount(0);
			await expectNoHorizontalOverflow(grafana);
			await expectNoSeriousA11yViolations(grafana);

			await grafana
				.getByRole("link", { name: "Go to endpoint registry" })
				.click();
			await expect(grafana).toHaveURL(/\/registry$/);
		});
	});

	test.describe("with the routing stack down", () => {
		test.use({ scenario: "registry:stack-down" });

		test("says the gateway is unreachable", async ({ page }) => {
			await page.goto("/registry");

			await expect(
				page.getByRole("heading", { name: "Gateway unreachable" }),
			).toBeVisible();
			await expect(
				page.getByRole("button", { name: "Try again" }),
			).toBeVisible();
			await expectNoHorizontalOverflow(page);
			await expectNoSeriousA11yViolations(page);
		});
	});

	test.describe("when the gateway goes down after loading", () => {
		test.use({ scenario: "registry:goes-down" });

		test("keeps the last registry and says as of when", async ({ page }) => {
			await page.goto("/registry");
			await expect(page.getByRole("table")).toBeVisible();

			// The next poll, 5 s later, fails.
			await expect(
				page.getByText(/Gateway unreachable\. Showing the registry as of/),
			).toBeVisible({ timeout: 15_000 });
			await expect(page.getByRole("table")).toBeVisible();
		});
	});

	test("adapts across every breakpoint without reloading", async ({
		page,
	}, testInfo) => {
		test.skip(
			testInfo.project.name !== "desktop",
			"walks every width itself; once is enough",
		);
		await page.goto(`/registry?model=${encodeURIComponent(LLAMA)}`);
		const table = page.getByRole("table");
		const panel = page.getByRole("region", { name: LLAMA });
		const nav = page.getByRole("tab", { name: "Endpoint registry" });
		const host = page.getByText("llm-gateway.example.com", { exact: true });
		await expect(panel).toBeVisible();

		for (const width of RESIZE_WIDTHS) {
			await page.setViewportSize({ width, height: 900 });
			await expectNoHorizontalOverflow(page);

			const tableBox = await table.boundingBox();
			const panelBox = await panel.boundingBox();
			if (!tableBox || !panelBox) throw new Error(`no layout at ${width}px`);
			if (width >= BREAKPOINTS.lg) {
				expect(panelBox.x, `side by side at ${width}px`).toBeGreaterThanOrEqual(
					tableBox.x + tableBox.width,
				);
			} else {
				expect(panelBox.y, `stacked at ${width}px`).toBeGreaterThanOrEqual(
					tableBox.y + tableBox.height,
				);
			}
			await (width >= BREAKPOINTS.sm
				? expect(nav).toBeVisible()
				: expect(nav).toBeHidden());
			await (width >= BREAKPOINTS.md
				? expect(host).toBeVisible()
				: expect(host).toBeHidden());
		}
	});
});
