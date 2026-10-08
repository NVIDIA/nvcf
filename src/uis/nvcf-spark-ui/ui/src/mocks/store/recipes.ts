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

import type { Recipe } from "~/generated/model/recipe";
import type { RecipeCatalog } from "~/generated/model/recipeCatalog";
import type { RecipeProfile } from "~/generated/model/recipeProfile";
import type { RecipeValidation } from "~/generated/model/recipeValidation";

/**
 * The recipe catalog behind the mocks, shaped like the real one (the recipe
 * charts' index.json passed through helm/recipes/catalog.jq). One build is
 * mock-only: Nemotron 5 Super 49B on DGX Station, so the hardware filter has
 * a Station recipe to find. Served model names match the mock registry, so
 * Qwen3.8-27B, GLM-5.3 and Nemotron 5 Super show as deployed.
 */

const GiB = 2 ** 30;
const SITE_VALUES = [
	"nodes",
	"runtimeClassName",
	"storageClassName",
	"sharedCAConfigMap",
];
const SGLANG = {
	backend: "sglang",
	version: "SGLang v0.5.19",
	image:
		"lmsysorg/sglang@sha256:4cba07b0c68725991890c64843403396effb7faa39ac47261166b2299095c513",
};

function profile(
	recipe: string,
	id: string,
	options: {
		chart?: string;
		nodes?: number;
		product?: string;
		memoryGiB: number;
		validation: RecipeValidation;
		nvme?: boolean;
		fabricGbps?: number;
		maxContext?: number;
	},
): RecipeProfile {
	const nodes = options.nodes ?? 1;
	const capabilities = options.nvme || options.fabricGbps !== undefined;
	return {
		id,
		hardware: {
			gpuProducts: [options.product ?? "NVIDIA-GB10"],
			gpuCount: 1,
			architecture: "arm64",
			memoryMode: "unified",
		},
		modelNodeCount: nodes,
		gpusPerNode: 1,
		perNode: Array.from({ length: nodes }, (_, rank) => ({
			rank,
			role: "model",
			resources: { memoryRequestBytes: options.memoryGiB * GiB, gpuRequest: 1 },
			storage: {
				claimRequestBytes: 80 * GiB,
				...(options.nvme ? { offloadMedium: "local-nvme" } : {}),
			},
		})),
		fabric:
			options.fabricGbps === undefined
				? null
				: { minimumGbps: options.fabricGbps },
		workload: {
			defaultContextTokens: 8192,
			defaultConcurrency: 1,
			maximumContextTokens: options.maxContext ?? 9216,
			maximumConcurrency: 1,
		},
		validation: options.validation,
		deployment: {
			chart: {
				name: options.chart ?? "pylon-sglang-recipe",
				version: "0.2.0",
				repository: "oci://nvcr.io/org",
			},
			lifecycle: "automatic",
			values: { recipe, profileName: id },
			requiredSiteValues: capabilities
				? [...SITE_VALUES, "nodeCapabilities"]
				: SITE_VALUES,
		},
	};
}

const smokeTested: RecipeValidation = {
	runtimeStatus: "smoke-tested",
	workload: { date: "2026-10-06", contextLength: 8192, concurrency: 1 },
	automaticHelmStatus: "smoke-tested",
	automaticHelmWorkload: {
		date: "2026-10-07",
		contextLength: 8192,
		concurrency: 1,
	},
};

function unavailable(
	id: string,
	name: string,
	status: string,
	reason: string,
	description?: string,
): Recipe {
	return {
		id,
		name,
		...(description ? { description } : {}),
		servedModelId: null,
		availability: {
			status,
			deployable: false,
			reason,
			checkedAt: "2026-10-07",
		},
		precision: null,
		license: null,
		model: null,
		runtime: null,
		profiles: [],
	};
}

export const recipeCatalog: RecipeCatalog = {
	schemaVersion: 1,
	recipes: [
		unavailable(
			"deepseek-v4-flash",
			"DeepSeek V4 Flash",
			"planned",
			"The verified checkpoint has upstream four-GPU candidates. Installation requires tensor-parallel chart support.",
			"Low-latency serving build",
		),
		{
			id: "glm-5.3",
			name: "GLM-5.3",
			description: "General reasoning, long context",
			servedModelId: "GLM-5.3-UD-IQ2_M",
			availability: { status: "available", deployable: true },
			precision: "UD-IQ2_M",
			license: "glm-5.3",
			model: { repository: "unsloth/GLM-5.3-GGUF", revision: "346b359" },
			runtime: { backend: "llama.cpp", version: "llama.cpp f872b59" },
			profiles: [
				profile("glm-5.3", "gb10-x2", {
					chart: "pylon-gguf-backend",
					nodes: 2,
					memoryGiB: 110,
					maxContext: 2048,
					validation: {
						runtimeStatus: "hardware-validated",
						automaticHelmStatus: "failed",
						automaticHelmWorkload: {
							date: "2026-10-07",
							contextLength: 2048,
							concurrency: 1,
							reason: "host_available",
						},
					},
				}),
			],
		},
		unavailable(
			"nemotron-5-nano-12b",
			"Nemotron 5 Nano 12B",
			"unavailable",
			"Exact-name public artifact and access status remain unverified.",
			"Compact reasoning model",
		),
		{
			id: "nemotron-5-super-49b",
			name: "Nemotron 5 Super 49B",
			description: "49B parameters, agentic tasks",
			servedModelId: "nvidia/nemotron-5-super-49b",
			availability: { status: "available", deployable: true },
			precision: "NVFP4",
			license: "NVIDIA Open Model License",
			model: { repository: "nvidia/Nemotron-5-Super-49B-NVFP4" },
			runtime: SGLANG,
			profiles: [
				profile("nemotron-5-super-49b", "station-gb300-x2", {
					nodes: 2,
					product: "NVIDIA-GB300",
					memoryGiB: 240,
					validation: smokeTested,
				}),
			],
		},
		{
			id: "qwen3.8-27b",
			name: "Qwen3.8-27B",
			description: "Instruction-tuned, 27B parameters",
			servedModelId: "qwen/qwen3.8-27b",
			availability: { status: "available", deployable: true },
			precision: "FP8",
			license: "Apache-2.0",
			model: { repository: "Qwen/Qwen3.8-27B-FP8", revision: "017b9c7" },
			runtime: SGLANG,
			profiles: [
				profile("qwen3.8-27b", "spark-fp8", {
					memoryGiB: 104,
					validation: smokeTested,
				}),
			],
		},
		{
			id: "qwen3.8-27b-nvfp4",
			name: "Qwen3.8-27B",
			description: "Instruction-tuned, 27B parameters",
			servedModelId: "qwen3.8-27b-nvfp4",
			availability: { status: "available", deployable: true },
			precision: "NVFP4",
			license: "Apache-2.0",
			model: { repository: "nvidia/Qwen3.8-27B-NVFP4", revision: "482ca0f" },
			runtime: SGLANG,
			profiles: [
				profile("qwen3.8-27b-nvfp4", "spark-nvfp4", {
					memoryGiB: 104,
					validation: smokeTested,
				}),
			],
		},
		unavailable(
			"qwen3.8-4b",
			"Qwen3.8-4B",
			"unavailable",
			"Exact-name public artifact and access status remain unverified.",
		),
		{
			id: "qwen3.8-flash-next",
			name: "Qwen3.8-Flash-Next",
			description: "Preview build",
			servedModelId: "qwen3.8-flash-next",
			availability: { status: "available", deployable: true },
			precision: "NVFP4",
			license: "Qwen Community 1.0",
			model: { repository: "RadixArk/Qwen3.8-Flash-Next-NVFP4" },
			runtime: {
				backend: "sglang",
				version: "SGLang dev-qwen38-next-local",
			},
			profiles: [
				profile("qwen3.8-flash-next", "spark-nvfp4-nvme", {
					memoryGiB: 110,
					nvme: true,
					maxContext: 262_144,
					validation: smokeTested,
				}),
				profile("qwen3.8-flash-next", "spark-nvfp4-tp2", {
					nodes: 2,
					memoryGiB: 110,
					fabricGbps: 200,
					maxContext: 262_144,
					validation: {
						runtimeStatus: "pending",
						workload: null,
						automaticHelmStatus: "pending",
						automaticHelmWorkload: null,
					},
				}),
			],
		},
	],
};
