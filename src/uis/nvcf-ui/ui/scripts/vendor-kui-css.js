// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { createRequire } from "node:module";
import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

const require = createRequire(import.meta.url);
const { version } = require("@nvidia/foundations-react-core/package.json");
const outDir = join(import.meta.dirname, "../vendor/kui-foundations");
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
mkdirSync(outDir, { recursive: true });

await Promise.all(
	files.map(async (file) => {
		const res = await fetch(`${cdn}/${file}`);
		if (!res.ok) throw new Error(`${file}: ${res.status}`);
		writeFileSync(join(outDir, file), await res.text());
	}),
);

writeFileSync(versionFile, version);
console.log(`Vendored KUI foundations CSS v${version}`);
