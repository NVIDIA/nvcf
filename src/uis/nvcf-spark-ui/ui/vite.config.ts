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

import tailwindcss from "@tailwindcss/vite";
import { devtools } from "@tanstack/devtools-vite";
import viteReact from "@vitejs/plugin-react";
import { coverageConfigDefaults, defineConfig } from "vitest/config";
import { bffBuild } from "./scripts/bff-build";

// Without mocks, the dev server forwards API calls to a BFF running locally
// (`make backend-run`), which serves on 8300 by default.
const bff = process.env.BFF_URL ?? "http://localhost:8300";

// Bazel-only Vitest overrides (set by //ui:test after the monorepo port):
// rules_js's symlinked runfiles need fs.strict off to serve the setup file and
// preserveSymlinks on to dedupe the vitest/expect instance. Inert otherwise.
const bazelVitest = process.env.BAZEL_VITEST === "1";

const config = defineConfig({
	// Under Bazel, `~/` resolves from the working directory, which //ui:test
	// sets to the package in its runfiles. Vite resolves tsconfig paths from
	// the tsconfig's real path, the bin-dir copy, so `~/` imports would load a
	// second module graph, with a second React, next to the relative imports.
	resolve: bazelVitest
		? {
				alias: [{ find: /^~\//, replacement: `${process.cwd()}/src/` }],
				preserveSymlinks: true,
			}
		: { tsconfigPaths: true },
	plugins: [devtools(), tailwindcss(), viteReact(), bffBuild()],
	server: {
		...(bazelVitest ? { fs: { strict: false } } : {}),
		proxy: {
			"/api": bff,
			"/v1": bff,
		},
	},
	build: {
		emptyOutDir: true,
	},
	test: {
		environment: "happy-dom",
		globals: true,
		include: ["src/**/*.test.{ts,tsx}"],
		setupFiles: ["./vitest.setup.ts"],
		// The playground's tests stream a reply and render it as markdown: about
		// 2 s each, but past Vitest's 5 s default on a busy machine.
		testTimeout: 15_000,
		coverage: {
			provider: "v8",
			reporter: ["text", "cobertura"],
			include: ["src/**"],
			exclude: [
				...coverageConfigDefaults.exclude,
				"src/generated/**",
				"src/mocks/**",
				"src/testing/**",
				"src/main.tsx",
				"src/router.tsx",
				"src/rootRoute.tsx",
			],
			thresholds: {
				lines: 80,
				branches: 80,
			},
		},
	},
});

export default config;
