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

import { mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { defineConfig, devices } from "@playwright/test";

// The production bundle behind the real BFF, with a fake gateway behind that
// (`make e2e-prod`, after the UI is built into dist/). It covers what the
// mock-mode suite can't: the BFF's headers and CSP, compressed assets, key
// injection and streaming through the proxy.
const BFF_PORT = 5298;
const GATEWAY_PORT = 5299;

// Not a secret: the fake gateway accepts only this key, which proves the BFF
// sends the one it was given.
const KEY = "e2e-not-a-secret";
const keyDir = join(tmpdir(), "nvcf-spark-ui-e2e");
mkdirSync(keyDir, { recursive: true });
const keyFile = join(keyDir, "api-key");
writeFileSync(keyFile, KEY);

export default defineConfig({
	testDir: "./e2e/prod",
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: process.env.CI ? 1 : 0,
	reporter: "list",
	use: {
		baseURL: `http://localhost:${BFF_PORT}`,
		trace: "retain-on-failure",
	},
	projects: [
		{ name: "mobile", use: { ...devices["Pixel 7"] } },
		{
			name: "desktop",
			use: {
				...devices["Desktop Chrome"],
				viewport: { width: 1920, height: 1080 },
			},
		},
	],
	webServer: [
		{
			command: "node e2e/prod/fake-gateway.mjs",
			url: `http://127.0.0.1:${GATEWAY_PORT}/healthz`,
			env: { PORT: String(GATEWAY_PORT), KEY },
			reuseExistingServer: false,
		},
		{
			command: "go run ./cmd/server",
			cwd: "../backend",
			url: `http://localhost:${BFF_PORT}/status`,
			timeout: 120_000,
			reuseExistingServer: false,
			env: {
				SERVER_PORT: String(BFF_PORT),
				STATIC_DIR: resolve("dist"),
				GATEWAY_URL: `http://127.0.0.1:${GATEWAY_PORT}`,
				GATEWAY_API_KEY_PATH: keyFile,
				GATEWAY_PUBLIC_URL: "https://llm-gateway.example.com",
				GRAFANA_URL: "https://grafana.example.com/d/llm-demo",
				// The recipe charts' catalog as the ConfigMap holds it.
				RECIPE_CATALOG_PATH: resolve("../backend/cmd/server/testdata/recipes.json"),
			},
		},
	],
});
