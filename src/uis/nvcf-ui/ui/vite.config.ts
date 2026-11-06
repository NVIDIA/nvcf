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
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { visualizer } from "rollup-plugin-visualizer";
import { coverageConfigDefaults, defineConfig } from "vitest/config";

// Bazel-only Vitest overrides (set by //ui:test): rules_js's symlinked runfiles
// need fs.strict off to serve the setup file and preserveSymlinks on to dedupe
// the vitest/expect instance. Inert for normal dev/build/`task test`.
const bazelVitest = process.env.BAZEL_VITEST === "1";

// The router plugin rewrites src/routeTree.gen.ts on every run, so its license
// header has to come from the plugin. Keep in sync with ../license-header.txt.
// A custom header replaces the plugin defaults, so they are repeated after it.
// The generator separates entries with a blank line, so the license is one entry.
const ROUTE_TREE_FILE_HEADER = [
	[
		"/**",
		" * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.",
		" * SPDX-License-Identifier: Apache-2.0",
		" *",
		' * Licensed under the Apache License, Version 2.0 (the "License");',
		" * you may not use this file except in compliance with the License.",
		" * You may obtain a copy of the License at",
		" *",
		" *     http://www.apache.org/licenses/LICENSE-2.0",
		" *",
		" * Unless required by applicable law or agreed to in writing, software",
		' * distributed under the License is distributed on an "AS IS" BASIS,',
		" * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.",
		" * See the License for the specific language governing permissions and",
		" * limitations under the License.",
		" */",
	].join("\n"),
	"/* eslint-disable */",
	"// @ts-nocheck",
	"// noinspection JSUnusedGlobalSymbols",
];

const config = defineConfig({
	resolve: {
		tsconfigPaths: true,
		...(bazelVitest ? { preserveSymlinks: true } : {}),
	},
	plugins: [
		devtools(),
		tanstackRouter({
			target: "react",
			autoCodeSplitting: true,
			// Let test files sit beside route files without becoming routes.
			// Mirrors Vitest's default include (*.test.* and *.spec.*).
			routeFileIgnorePattern: "\\.(test|spec)\\.[cm]?[jt]sx?$",
			routeTreeFileHeader: ROUTE_TREE_FILE_HEADER,
		}),
		tailwindcss(),
		viteReact(),
		process.env.ANALYZE === "true" &&
			visualizer({ open: true, filename: "bundle-report.html" }),
	],
	server: {
		...(bazelVitest ? { fs: { strict: false } } : {}),
		proxy: {
			"/api": {
				target: "http://localhost:8080",
				configure: (proxy) => {
					proxy.on("error", () => {});
				},
			},
		},
	},
	build: {
		emptyOutDir: true,
	},
	test: {
		environment: "happy-dom",
		globals: true,
		setupFiles: ["./vitest.setup.ts"],
		coverage: {
			provider: "v8",
			reporter: ["text", "cobertura"],
			exclude: [
				...coverageConfigDefaults.exclude,
				"src/assets/**",
				"src/generated/**",
				"src/mocks/**",
				"src/testing/**",
				"src/orval.config.ts",
				"src/orval-transformer.ts",
				"src/main.tsx",
				"src/router.tsx",
				"src/routes/__root.tsx",
				"src/routeTree.gen.ts",
			],
		},
	},
});

export default config;
