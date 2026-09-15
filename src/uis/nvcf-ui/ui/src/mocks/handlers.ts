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

import type { HttpHandler } from "msw";
import { getStoreHandlers } from "./store";

/**
 * All scenarios should be added to the `scenarios` directory -- each folder representing a feature,
 * and each file representing a scenario for the given feature.
 *
 * To enable one or more scenarios, set the `VITE_SCENARIO` environment variable to a comma-separated list of `feature:file` values.
 * For example: `VITE_SCENARIO=functions:empty-list,tasks:lauching-task` would enable the `empty-list` scenario for the `functions` feature.
 */

const scenarioModules = import.meta.glob<{ handlers: HttpHandler[] }>(
	"./scenarios/**/*.ts",
);

/**
 * Baseline handlers used by both the browser worker and the Node test server.
 * Backed by the seeded store so data is stable and referentially consistent
 * (list ids match detail, everything keyed by the seeded account ncaIds).
 * `VITE_SCENARIO` overrides layer on top of these (see `getHandlers`).
 */
export const defaultHandlers: HttpHandler[] = getStoreHandlers();

export async function getHandlers(): Promise<HttpHandler[]> {
	const scenarios =
		import.meta.env.VITE_SCENARIO?.split(",").filter(Boolean) ?? [];
	const overrides: HttpHandler[] = [];

	for (const name of scenarios) {
		const [feature, file] = name.split(":");
		const loader = scenarioModules[`./scenarios/${feature}/${file}.ts`];
		if (!loader) {
			console.warn(`Unknown scenario: ${name}`);
			continue;
		}
		const { handlers } = await loader();
		overrides.push(...handlers);
	}
	return overrides.concat(defaultHandlers);
}
