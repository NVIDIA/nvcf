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

import type { Page, TestInfo } from "@playwright/test";
import {
	BREAKPOINTS,
	expect,
	expectNoHorizontalOverflow,
	expectNoSeriousA11yViolations,
	RESIZE_WIDTHS,
	test,
} from "./fixtures";

const FLASH_TP2 =
	"/recipes/qwen3.8-flash-next?config=qwen3.8-flash-next%2Fspark-nvfp4-tp2";

/**
 * How many recipes match, as announced to screen readers; the page doesn't
 * show it. The shell's gateway status is a status region too.
 */
function recipeCount(page: Page) {
	return page.getByRole("status").filter({ hasText: "recipe" });
}

/** Goes to the recipes from the app bar: the nav, or below sm its drawer. */
async function openRecipesFromNav(page: Page, testInfo: TestInfo) {
	if (testInfo.project.name === "mobile") {
		await expect(
			page.getByRole("tab", { name: "Model deployment recipes" }),
		).toBeHidden();
		await page.getByRole("button", { name: "Open navigation" }).click();
		const drawer = page.getByRole("dialog", { name: "Navigation" });
		await drawer
			.getByRole("link", { name: "Model deployment recipes" })
			.click();
		await expect(drawer).toBeHidden();
	} else {
		await page.getByRole("tab", { name: "Model deployment recipes" }).click();
	}
	await expect(page).toHaveURL(/\/recipes$/);
}

test.describe("model deployment recipes", () => {
	test("finds a recipe, picks a configuration and copies its install command", async ({
		page,
		context,
	}, testInfo) => {
		await context.grantPermissions(["clipboard-read", "clipboard-write"]);
		await page.goto("/registry");
		await openRecipesFromNav(page, testInfo);

		await page
			.getByRole("searchbox", { name: "Search recipes" })
			.fill("qwen3.8-27b");
		await expect(recipeCount(page)).toHaveText("1 of 7 recipes");
		await expect(page).toHaveURL(/[?&]q=qwen3\.8-27b/);
		await page.getByRole("link", { name: /Qwen3\.8-27B/ }).click();

		await expect(page).toHaveURL(/\/recipes\/qwen3\.8-27b/);
		await expect(
			page.getByRole("heading", { level: 1, name: "Qwen3.8-27B" }),
		).toBeVisible();
		await page.getByRole("radio", { name: /NVFP4/ }).check();
		await expect(page).toHaveURL(/config=qwen3\.8-27b-nvfp4%2Fspark-nvfp4/);

		const steps = page.getByRole("region", { name: "Deployment steps" });
		await steps.getByRole("button", { name: "Copy code" }).click();
		await expect
			.poll(() => page.evaluate(() => navigator.clipboard.readText()))
			.toContain("--set-string profileName=spark-nvfp4");
	});

	test("filters by hardware from the toolbar", async ({ page }) => {
		await page.goto("/recipes");

		await page.getByRole("combobox", { name: "Filter by hardware" }).click();
		await page.getByRole("option", { name: "DGX Station" }).click();

		await expect(recipeCount(page)).toHaveText("1 of 7 recipes");
		await expect(page).toHaveURL(/hardware=DGX(\+|%20)Station/);
		await expect(
			page.getByRole("heading", { level: 2, name: "Nemotron 5 Super 49B" }),
		).toBeVisible();
	});

	test("opens deep links, and keeps them on refresh", async ({ page }) => {
		await page.goto("/recipes?hardware=DGX%20Station");
		await expect(recipeCount(page)).toHaveText("1 of 7 recipes");
		await page.reload();
		await expect(recipeCount(page)).toHaveText("1 of 7 recipes");

		await page.goto(FLASH_TP2);
		const tp2 = page.getByRole("radio", { name: /DGX Spark × 2/ });
		await expect(tp2).toBeChecked();
		await page.reload();
		await expect(tp2).toBeChecked();
		await expect(
			page.getByRole("region", { name: "Deployment steps" }),
		).toContainText('"address":"<node-2-fabric-address>"');
	});

	test("links a deployed model to its endpoint", async ({ page }) => {
		await page.goto("/recipes/qwen3.8-27b");

		await page
			.getByRole("link", {
				name: "View qwen/qwen3.8-27b in the endpoint registry",
			})
			.click();
		await expect(page).toHaveURL(/\/registry\?model=qwen%2Fqwen3\.8-27b$/);
	});

	test("puts every card's status badge under its name", async ({ page }) => {
		await page.goto("/recipes");
		// Deployed markers come from the registry, which may load last.
		await expect(
			page.getByText("Deployed", { exact: true }).first(),
		).toBeVisible();

		const cards = page
			.getByRole("link")
			.filter({ has: page.getByRole("heading", { level: 2 }) });
		let badges = 0;
		for (const card of await cards.all()) {
			const badge = card.getByText(/^(Not available|Deployed(, unhealthy)?)$/);
			if ((await badge.count()) === 0) continue;
			const name = await card.getByRole("heading", { level: 2 }).boundingBox();
			const status = await badge.boundingBox();
			if (!name || !status) throw new Error("a card has no layout box");
			expect(status.y).toBeGreaterThanOrEqual(name.y + name.height - 1);
			expect(status.x).toBeCloseTo(name.x, 0);
			badges += 1;
		}
		expect(badges).toBeGreaterThan(1);
	});

	test("fits the viewport and passes axe", async ({ page }) => {
		await page.goto("/recipes");
		await expect(recipeCount(page)).toHaveText("7 recipes");
		await expectNoHorizontalOverflow(page);
		await expectNoSeriousA11yViolations(page);

		await page.goto("/recipes/glm-5.3");
		await expect(
			page.getByRole("heading", { level: 1, name: "GLM-5.3" }),
		).toBeVisible();
		await expectNoHorizontalOverflow(page);
		await expectNoSeriousA11yViolations(page);
	});

	test("says when nothing matches", async ({ page }) => {
		await page.goto("/recipes?q=zzz");

		await expect(
			page.getByRole("heading", { name: "No recipes match" }),
		).toBeVisible();
		await page.getByRole("button", { name: "Clear search" }).click();
		await expect(recipeCount(page)).toHaveText("7 recipes");
		await expect(page).toHaveURL(/\/recipes$/);
	});

	test.describe("while the catalog loads", () => {
		test.use({ scenario: "recipes:loading" });

		test("shows the heading and card placeholders", async ({ page }) => {
			await page.goto("/recipes");

			await expect(
				page.getByRole("heading", { name: "Model deployment recipes" }),
			).toBeVisible();
			await expect(page.getByText("Loading recipes")).toBeAttached();
			await expectNoHorizontalOverflow(page);
		});
	});

	test.describe("with an empty catalog", () => {
		test.use({ scenario: "recipes:empty" });

		test("explains it", async ({ page }) => {
			await page.goto("/recipes");

			await expect(
				page.getByRole("heading", { name: "No recipes yet" }),
			).toBeVisible();
			await expectNoHorizontalOverflow(page);
			await expectNoSeriousA11yViolations(page);
		});
	});

	test.describe("with no catalog mounted", () => {
		test.use({ scenario: "recipes:unavailable" });

		test("says so, and offers a retry", async ({ page }) => {
			await page.goto("/recipes");

			await expect(
				page.getByRole("heading", { name: "Recipe catalog unavailable" }),
			).toBeVisible();
			await expect(
				page.getByRole("button", { name: "Try again" }),
			).toBeVisible();
			await expectNoHorizontalOverflow(page);
			await expectNoSeriousA11yViolations(page);
		});
	});

	test("adapts across every breakpoint without reloading", async ({
		page,
	}, testInfo) => {
		test.skip(
			testInfo.project.name !== "desktop",
			"walks every width itself; once is enough",
		);
		await page.goto(FLASH_TP2);
		const configs = page.getByRole("region", {
			name: "Hardware configurations",
		});
		const steps = page.getByRole("region", { name: "Deployment steps" });
		const nav = page.getByRole("tab", { name: "Model deployment recipes" });
		const expander = page.getByRole("button", { name: "Open navigation" });
		await expect(steps).toBeVisible();

		for (const width of RESIZE_WIDTHS) {
			await page.setViewportSize({ width, height: 900 });
			await expectNoHorizontalOverflow(page);

			const configsBox = await configs.boundingBox();
			const stepsBox = await steps.boundingBox();
			if (!configsBox || !stepsBox) throw new Error(`no layout at ${width}px`);
			if (width >= BREAKPOINTS.lg) {
				expect(stepsBox.x, `side by side at ${width}px`).toBeGreaterThanOrEqual(
					configsBox.x + configsBox.width,
				);
			} else {
				expect(stepsBox.y, `stacked at ${width}px`).toBeGreaterThanOrEqual(
					configsBox.y + configsBox.height,
				);
			}
			if (width >= BREAKPOINTS.sm) {
				await expect(nav).toBeVisible();
				await expect(expander).toBeHidden();
			} else {
				await expect(nav).toBeHidden();
				await expect(expander).toBeVisible();
			}
		}
	});
});
