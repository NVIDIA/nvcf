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

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { basename, join } from "node:path";

const require = createRequire(import.meta.url);
const { version } = require("@nvidia/foundations-react-core/package.json");
const outDir = join(import.meta.dirname, "../vendor/kui-foundations");
const fontsDir = join(outDir, "fonts");
const versionFile = join(outDir, ".version");
const files = ["base-external.css", "components.css"];

if (
	existsSync(versionFile) &&
	readFileSync(versionFile, "utf8").trim() === version &&
	files.every((f) => existsSync(join(outDir, f)))
) {
	console.log(`KUI foundations CSS v${version} already up to date`);
	process.exit(0);
}

const cdn = `https://webassets.nvidia.com/kaizen-ui-foundations/${version}`;
mkdirSync(fontsDir, { recursive: true });

// The demo runs with no external network, but
// base-external.css loads JetBrains Mono from a CDN. Every remote url() is
// downloaded into fonts/ and rewritten to the local copy, which Vite bundles.
const remoteUrl = /url\((https:\/\/[^)]+)\)/g;

async function download(url) {
	const res = await fetch(url);
	if (!res.ok) throw new Error(`${url}: ${res.status}`);
	return res;
}

await Promise.all(
	files.map(async (file) => {
		let css = await (await download(`${cdn}/${file}`)).text();
		const urls = [...new Set([...css.matchAll(remoteUrl)].map((m) => m[1]))];
		for (const url of urls) {
			const name = basename(new URL(url).pathname);
			const font = await download(url);
			writeFileSync(
				join(fontsDir, name),
				Buffer.from(await font.arrayBuffer()),
			);
			css = css.replaceAll(`url(${url})`, `url(./fonts/${name})`);
		}
		writeFileSync(join(outDir, file), css);
	}),
);

writeFileSync(versionFile, version);
console.log(`Vendored KUI foundations CSS v${version}`);
