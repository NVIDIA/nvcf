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

import { readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";
import type { Plugin } from "vite";

/** Text assets worth compressing; fonts and images are compressed already. */
const COMPRESSIBLE = /\.(?:html|js|css|json|svg|ico|txt|map)$/;
/** Below this, compression saves less than it costs to negotiate. */
const MIN_BYTES = 1024;

function* files(dir: string): Generator<string> {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) yield* files(path);
		else yield path;
	}
}

/**
 * Shapes the production build for the BFF that serves it:
 * - drops MSW's service worker, which only mock mode registers;
 * - writes brotli and gzip copies of text assets, which the BFF sends to
 *   browsers that accept them, so nothing is compressed per request.
 */
export function bffBuild(): Plugin {
	let outDir = "";
	return {
		name: "bff-build",
		apply: "build",
		configResolved(config) {
			outDir = resolve(config.root, config.build.outDir);
		},
		closeBundle() {
			if (process.env.VITE_MOCK !== "true") {
				rmSync(join(outDir, "mockServiceWorker.js"), { force: true });
			}
			for (const path of files(outDir)) {
				if (!COMPRESSIBLE.test(path)) continue;
				const source = readFileSync(path);
				if (source.length < MIN_BYTES) continue;
				const br = brotliCompressSync(source, {
					params: {
						[constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY,
						[constants.BROTLI_PARAM_SIZE_HINT]: source.length,
					},
				});
				const gz = gzipSync(source, { level: 9 });
				if (br.length < source.length) writeFileSync(`${path}.br`, br);
				if (gz.length < source.length) writeFileSync(`${path}.gz`, gz);
			}
		},
	};
}
