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
import { getGetConfigMockHandler } from "~/generated/api/config/config.msw";
import { getListModelsMockHandler } from "~/generated/api/models/models.msw";
import { getGetRecipesMockHandler } from "~/generated/api/recipes/recipes.msw";
import { getGetRegistryMockHandler } from "~/generated/api/registry/registry.msw";
import { RegistryHealth } from "~/generated/model/registryHealth";
import { chatCompletionHandler } from "../chat";
import { mockStore } from "./store";

/**
 * Store-backed default handlers for every route the UI calls. The registry is
 * stamped with the current time on each poll, as the gateway's is.
 */
export function getStoreHandlers(): HttpHandler[] {
	return [
		getGetRegistryMockHandler(({ request }) => {
			const model = new URL(request.url).searchParams.get("model");
			return {
				generatedAt: new Date().toISOString(),
				models:
					model === null
						? mockStore.registry
						: mockStore.registry.filter((m) => m.model === model),
			};
		}),
		getListModelsMockHandler(() => ({
			object: "list",
			data: mockStore.registry
				.filter((m) => m.health === RegistryHealth.Healthy)
				.map((m) => ({
					id: m.model,
					object: "model",
					created: mockStore.created,
					owned_by: "inference-endpoints",
				})),
		})),
		getGetConfigMockHandler(mockStore.config),
		getGetRecipesMockHandler(mockStore.recipes),
		chatCompletionHandler(),
	];
}
