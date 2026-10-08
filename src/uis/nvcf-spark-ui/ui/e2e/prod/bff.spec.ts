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

import { expect, expectNoSeriousA11yViolations, test } from "../fixtures";

// The production bundle, served by the real BFF, in front of the fake gateway
// in fake-gateway.mjs. The `sameOriginOnly` fixture also fails any test whose page
// reaches outside the BFF's origin.

const GLM = "GLM-5.3-UD-IQ2_M";

declare global {
	interface Window {
		cspViolations?: string[];
	}
}

test.beforeEach(async ({ page }) => {
	await page.addInitScript(() => {
		window.cspViolations = [];
		document.addEventListener("securitypolicyviolation", (event) => {
			window.cspViolations?.push(
				`${event.violatedDirective} ${event.blockedURI}`,
			);
		});
	});
});

test.afterEach(async ({ page }) => {
	// The BFF's Content-Security-Policy allows only same-origin content; the
	// bundle must never need more.
	expect(await page.evaluate(() => window.cspViolations ?? [])).toEqual([]);
});

test("serves the registry from the gateway, with the deployment's addresses", async ({
	page,
}) => {
	await page.goto("/");

	await expect(page).toHaveURL(/\/registry$/);
	const table = page.getByRole("table");
	await expect(table.getByRole("link", { name: GLM })).toBeVisible();
	await expect(
		table.getByRole("link", { name: "deepseek-ai/deepseek-v4-flash" }),
	).toBeVisible();
	const panel = page.getByRole("region", { name: GLM });
	await expect(panel).toContainText(
		'curl "https://llm-gateway.example.com/v1/chat/completions"',
	);
	await expect(
		panel.getByRole("link", { name: "Metrics in Grafana" }),
	).toHaveAttribute(
		"href",
		`https://grafana.example.com/d/llm-demo?var-model=${GLM}`,
	);
	await expectNoSeriousA11yViolations(page);
});

test("sends hardening headers and compressed, immutable assets", async ({
	page,
}) => {
	const script = page.waitForResponse(/\/assets\/index-[^/]+\.js$/);
	const document = await page.goto("/registry");
	if (!document) throw new Error("no response for the page");

	const headers = document.headers();
	expect(headers["content-security-policy"]).toContain("default-src 'self'");
	expect(headers["x-content-type-options"]).toBe("nosniff");
	expect(headers["x-frame-options"]).toBe("DENY");
	expect(headers["cache-control"]).toBe("no-store");

	const asset = (await script).headers();
	expect(asset["content-encoding"]).toBe("br");
	expect(asset["cache-control"]).toBe("public, max-age=31536000, immutable");
});

test("streams a chat reply through the BFF", async ({ page }) => {
	await page.goto(`/playground?model=${GLM}`);

	await page.getByRole("button", { name: "Send" }).click();

	await expect(page.getByText("Served through the BFF.")).toBeVisible();
	await expect(page.getByText(/6 tokens · first token/)).toBeVisible();
	await page.getByText(/^Thought for/).click();
	await expect(page.getByText("Answer briefly.")).toBeVisible();
});

test("keeps the browser's cookies away from the gateway", async ({
	page,
	context,
	baseURL,
}) => {
	await context.addCookies([
		{ name: "session", value: "browser-only", url: baseURL ?? "" },
	]);

	await page.goto(`/playground?model=${GLM}`);
	await page.getByRole("button", { name: "Send" }).click();

	// The fake gateway rejects any request that carries a cookie.
	await expect(page.getByText("Served through the BFF.")).toBeVisible();
});

test("serves deep links from the SPA fallback and API typos as JSON", async ({
	page,
	request,
}) => {
	await page.goto(
		`/playground?model=${encodeURIComponent("meta/llama-3.1-8b-instruct")}`,
	);
	await page.reload();
	await expect(
		page.getByRole("heading", { level: 1, name: "meta/llama-3.1-8b-instruct" }),
	).toBeVisible();

	const typo = await request.get("/api/v1/registry");
	expect(typo.status()).toBe(404);
	expect(await typo.json()).toEqual({ message: "Not Found" });
});

test("serves the recipe catalog, and its pages, through the BFF", async ({
	page,
	request,
}) => {
	const catalog = await request.get("/api/v1/recipes");
	expect(catalog.status()).toBe(200);
	expect(catalog.headers()["cache-control"]).toBe("no-cache");
	expect((await catalog.json()).recipes).toHaveLength(8);

	await page.goto("/recipes");
	// One card per model: the 8 builds are 7 models.
	await expect(page.getByRole("heading", { level: 2 })).toHaveCount(7);
	await expectNoSeriousA11yViolations(page);

	await page.goto("/recipes/qwen3.8-27b");
	await expect(
		page.getByRole("heading", { level: 1, name: "Qwen3.8-27B" }),
	).toBeVisible();
	await expect(
		page.getByRole("region", { name: "Deployment steps" }),
	).toContainText("oci://nvcr.io/org/pylon-sglang-recipe");
	await expectNoSeriousA11yViolations(page);
});

test("ships no mock service worker", async ({ request }) => {
	const worker = await request.get("/mockServiceWorker.js");
	// Unknown paths get the SPA's index.html, never the worker script.
	expect(await worker.text()).not.toContain("Mock Service Worker");
});

test("blocks content from other origins", async ({ context }) => {
	// A page of its own, outside the sameOriginOnly fixture: this test makes
	// the external request on purpose, to prove the policy stops it.
	const page = await context.newPage();
	await page.addInitScript(() => {
		window.cspViolations = [];
		document.addEventListener("securitypolicyviolation", (event) => {
			window.cspViolations?.push(
				`${event.violatedDirective} ${event.blockedURI}`,
			);
		});
	});
	await page.goto("/registry");
	await expect(page.getByRole("table")).toBeVisible();

	await page.evaluate(() => {
		const image = document.createElement("img");
		image.src = "https://cdn.example.com/pixel.png";
		document.body.append(image);
	});

	await expect
		.poll(() => page.evaluate(() => window.cspViolations ?? []))
		.toEqual(["img-src https://cdn.example.com/pixel.png"]);
});
