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

import { defineConfig, devices } from "@playwright/test";

// A port of its own, so a dev server you already run (without mocks) is never
// mistaken for the mock-mode server the tests need.
const PORT = 5199;

export default defineConfig({
	testDir: "./e2e",
	// e2e/prod runs against the production build (playwright.prod.config.ts).
	testIgnore: "prod/**",
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: process.env.CI ? 1 : 0,
	reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
	// @live tests need a deployed stack; they run only when LIVE is set.
	grepInvert: process.env.LIVE ? undefined : /@live/,
	use: {
		baseURL: `http://localhost:${PORT}`,
		trace: "retain-on-failure",
	},
	// Four viewports, all on Chromium.
	projects: [
		{ name: "mobile", use: { ...devices["Pixel 7"] } },
		{
			name: "tablet",
			use: { ...devices["iPad Mini"], browserName: "chromium" },
		},
		{
			name: "laptop",
			use: {
				...devices["Desktop Chrome"],
				viewport: { width: 1280, height: 800 },
			},
		},
		{
			name: "desktop",
			use: {
				...devices["Desktop Chrome"],
				viewport: { width: 1920, height: 1080 },
			},
		},
	],
	webServer: {
		// Vite directly, not through pnpm: whichever pnpm is on the path may not
		// be the version this project pins.
		command: `./node_modules/.bin/vite dev --port ${PORT} --strictPort`,
		url: `http://localhost:${PORT}`,
		reuseExistingServer: !process.env.CI,
		env: { VITE_MOCK: "true" },
	},
});
