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

import type { Config } from "~/generated/model/config";
import { RegistryHealth } from "~/generated/model/registryHealth";
import type { RegistryModel } from "~/generated/model/registryModel";
import { recipeCatalog } from "./recipes";

/**
 * The fixed dataset behind the default mock handlers: one cluster's
 * registrations, healthy and not, with realistic model names (bare names and
 * names with a `/`). Hand-written rather than faker-generated so screenshots,
 * tests and the demo walkthrough always see the same endpoints.
 */

const CLUSTER_ID = "spark-cluster";

function registration(model: string, healthy: boolean): RegistryModel {
	return {
		model,
		health: healthy ? RegistryHealth.Healthy : RegistryHealth.Unhealthy,
		clusters: [
			{
				clusterId: CLUSTER_ID,
				registeredServers: 1,
				healthyServers: healthy ? 1 : 0,
			},
		],
	};
}

// Sorted by model name, as the gateway returns them.
const registry: RegistryModel[] = [
	registration("GLM-5.3-UD-IQ2_M", true),
	registration("deepseek-ai/deepseek-v4-flash", false),
	registration("meta/llama-3.1-8b-instruct", true),
	registration("nvidia/nemotron-5-super-49b", false),
	registration("qwen/qwen3.8-27b", true),
	registration("qwen/qwen3.8-4b", true),
];

const config: Config = {
	gatewayUrl: "https://llm-gateway.example.com",
	grafanaUrl: "https://grafana.example.com/d/llm-demo",
};

export const mockStore = {
	registry,
	config,
	recipes: recipeCatalog,
	/** When the mock gateway first saw each model, for `/v1/models`. */
	created: 1_791_240_539,
};
