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

import { describe, expect, it } from "vitest";
import type { Recipe } from "~/generated/model/recipe";
import type { RecipeProfile } from "~/generated/model/recipeProfile";
import {
	chartReference,
	configurations,
	formatGiB,
	groupRecipes,
	helmInstallCommand,
	lastValidation,
	matchesHardware,
	matchesSearch,
	nodeCapabilities,
	recipeSlug,
	releaseName,
	validateRecipeSearch,
	validateRecipesSearch,
	validationLabel,
} from "./utils";

const GiB = 2 ** 30;

function profile(
	id: string,
	overrides: Partial<RecipeProfile> & { nodes?: number; product?: string } = {},
): RecipeProfile {
	const { nodes = 1, product = "NVIDIA-GB10", ...rest } = overrides;
	return {
		id,
		hardware: { gpuProducts: [product], gpuCount: 1 },
		modelNodeCount: nodes,
		gpusPerNode: 1,
		perNode: Array.from({ length: nodes }, (_, rank) => ({
			rank,
			role: "model",
			resources: { memoryRequestBytes: (104 + rank) * GiB },
		})),
		validation: { runtimeStatus: "smoke-tested" },
		deployment: {
			chart: {
				name: "pylon-sglang-recipe",
				version: "0.2.0",
				repository: "oci://nvcr.io/org",
			},
			lifecycle: "automatic",
			values: { recipe: "x", profileName: id },
			requiredSiteValues: [
				"nodes",
				"runtimeClassName",
				"storageClassName",
				"sharedCAConfigMap",
			],
		},
		...rest,
	};
}

function recipe(id: string, overrides: Partial<Recipe> = {}): Recipe {
	return {
		id,
		name: id,
		servedModelId: id,
		availability: { status: "available", deployable: true },
		precision: "FP8",
		profiles: [profile(`${id}-spark`)],
		...overrides,
	};
}

const qwenFp8 = recipe("qwen3.8-27b", {
	name: "Qwen3.8-27B",
	description: "Instruction-tuned, 27B parameters",
});
const qwenNvfp4 = recipe("qwen3.8-27b-nvfp4", {
	name: "Qwen3.8-27B",
	precision: "NVFP4",
});
const planned = recipe("nemotron-5-nano-12b", {
	name: "Nemotron 5 Nano 12B",
	servedModelId: null,
	precision: null,
	availability: { status: "unavailable", deployable: false },
	profiles: [],
});
const station = recipe("glm-5.3", {
	name: "GLM-5.3",
	profiles: [
		profile("gb10-x2", { nodes: 2 }),
		profile("gb300-x1", { product: "NVIDIA GB300" }),
	],
});

describe("recipeSlug", () => {
	it("keeps dots and folds everything else to dashes", () => {
		expect(recipeSlug("Qwen3.8-27B")).toBe("qwen3.8-27b");
		expect(recipeSlug("  Nemotron 5 Nano 12B ")).toBe("nemotron-5-nano-12b");
		expect(recipeSlug("A / B")).toBe("a-b");
		expect(recipeSlug("???")).toBe("");
	});
});

describe("groupRecipes", () => {
	it("makes one model per name, deployable ones first", () => {
		const models = groupRecipes([planned, qwenFp8, station, qwenNvfp4]);
		expect(models.map((m) => m.slug)).toEqual([
			"qwen3.8-27b",
			"glm-5.3",
			"nemotron-5-nano-12b",
		]);
		const [qwen] = models;
		expect(qwen?.builds.map((b) => b.id)).toEqual([
			"qwen3.8-27b",
			"qwen3.8-27b-nvfp4",
		]);
		expect(qwen?.description).toBe("Instruction-tuned, 27B parameters");
		expect(qwen?.deployable).toBe(true);
	});

	it("lists the systems its builds support, in a fixed order", () => {
		const [glm] = groupRecipes([station]);
		expect(glm?.hardware).toEqual(["DGX Spark", "DGX Station"]);
		const [nano] = groupRecipes([planned]);
		expect(nano?.hardware).toEqual([]);
		expect(nano?.deployable).toBe(false);
	});

	it("falls back to the id for a name with nothing to slug", () => {
		const [odd] = groupRecipes([recipe("odd-1", { name: "???" })]);
		expect(odd?.slug).toBe("odd-1");
	});

	it("takes the first description any build has", () => {
		const [model] = groupRecipes([
			recipe("a", { name: "M" }),
			recipe("b", { name: "M", description: "Second" }),
		]);
		expect(model?.description).toBe("Second");
	});
});

describe("configurations", () => {
	it("pairs every deployable build with each of its profiles", () => {
		const [glm] = groupRecipes([station]);
		if (!glm) throw new Error("no model");
		const configs = configurations(glm);
		expect(configs.map((c) => c.key)).toEqual([
			"glm-5.3/gb10-x2",
			"glm-5.3/gb300-x1",
		]);
		expect(configs[0]).toMatchObject({
			hardware: "DGX Spark",
			nodeCount: 2,
			memoryPerNodeBytes: 105 * GiB,
		});
		expect(configs[1]?.hardware).toBe("DGX Station");
	});

	it("leaves out builds that can't be deployed", () => {
		const [nano] = groupRecipes([planned]);
		if (!nano) throw new Error("no model");
		expect(configurations(nano)).toEqual([]);
	});

	it("has no memory figure for a profile with no nodes listed", () => {
		const [m] = groupRecipes([
			recipe("m", { profiles: [profile("p", { perNode: [] })] }),
		]);
		if (!m) throw new Error("no model");
		expect(configurations(m)[0]?.memoryPerNodeBytes).toBeUndefined();
	});

	it("has no hardware class for an unknown GPU", () => {
		const [m] = groupRecipes([
			recipe("m", { profiles: [profile("p", { product: "NVIDIA-B200" })] }),
		]);
		if (!m) throw new Error("no model");
		expect(configurations(m)[0]?.hardware).toBeUndefined();
		expect(m.hardware).toEqual([]);
	});
});

describe("matchesSearch and matchesHardware", () => {
	const [qwen, glm, nano] = groupRecipes([
		qwenFp8,
		qwenNvfp4,
		station,
		planned,
	]);

	it("matches every term against names, ids and precision", () => {
		if (!qwen || !nano) throw new Error("no model");
		expect(matchesSearch(qwen, "")).toBe(true);
		expect(matchesSearch(qwen, "qwen nvfp4")).toBe(true);
		expect(matchesSearch(qwen, "INSTRUCTION")).toBe(true);
		expect(matchesSearch(qwen, "qwen glm")).toBe(false);
		expect(matchesSearch(nano, "nano")).toBe(true);
	});

	it("filters by system, or not at all", () => {
		if (!qwen || !glm) throw new Error("no model");
		expect(matchesHardware(qwen, undefined)).toBe(true);
		expect(matchesHardware(qwen, "DGX Spark")).toBe(true);
		expect(matchesHardware(qwen, "DGX Station")).toBe(false);
		expect(matchesHardware(glm, "DGX Station")).toBe(true);
	});
});

describe("formatting", () => {
	it("states memory in whole GiB", () => {
		expect(formatGiB(111_669_149_696)).toBe("104 GiB");
	});

	it("reports the one-step install's run first", () => {
		const helm = { date: "2026-10-07" };
		const runtime = { date: "2026-10-06" };
		expect(
			lastValidation(
				profile("p", {
					validation: {
						runtimeStatus: "smoke-tested",
						workload: runtime,
						automaticHelmWorkload: helm,
					},
				}),
			),
		).toBe(helm);
		expect(
			lastValidation(
				profile("p", {
					validation: {
						runtimeStatus: "smoke-tested",
						workload: runtime,
						automaticHelmWorkload: null,
					},
				}),
			),
		).toBe(runtime);
		expect(lastValidation(profile("p"))).toBeUndefined();
	});

	it("builds the chart reference from the catalog's repository", () => {
		expect(
			chartReference({
				name: "pylon-sglang-recipe",
				version: "0.2.0",
				repository: "oci://nvcr.io/org/",
			}),
		).toBe("oci://nvcr.io/org/pylon-sglang-recipe");
		expect(chartReference({ name: "c", version: "1" })).toBe(
			"oci://<chart-registry>/c",
		);
	});

	it("names the release after the build, as the planner does", () => {
		expect(releaseName(qwenFp8)).toBe("qwen3-8-27b");
	});
});

describe("validationLabel", () => {
	const withValidation = (
		runtimeStatus: string,
		automaticHelmStatus?: string,
	) =>
		validationLabel(
			profile("p", {
				validation: {
					runtimeStatus,
					...(automaticHelmStatus ? { automaticHelmStatus } : {}),
				},
			}),
		);

	it("reports the one-step install's outcome, else the runtime's", () => {
		expect(withValidation("pending", "smoke-tested")).toEqual({
			label: "Tested",
			color: "green",
		});
		expect(withValidation("hardware-validated")).toEqual({
			label: "Tested",
			color: "green",
		});
		expect(withValidation("smoke-tested", "pending")).toEqual({
			label: "Not tested yet",
			color: "gray",
		});
	});

	it("shows nothing for a failed install, and unknown statuses as they are", () => {
		expect(withValidation("hardware-validated", "failed")).toBeUndefined();
		expect(withValidation("benchmarked")).toEqual({
			label: "benchmarked",
			color: "gray",
		});
	});
});

describe("deployment snippets", () => {
	const [glm] = groupRecipes([station]);
	if (!glm) throw new Error("no model");
	const [twoNode] = configurations(glm);
	if (!twoNode) throw new Error("no configuration");

	it("writes the planner's helm install, quoting placeholders for the shell", () => {
		expect(helmInstallCommand(twoNode)).toBe(
			[
				"helm install glm-5-3 oci://nvcr.io/org/pylon-sglang-recipe",
				"--version 0.2.0",
				"--namespace llm-stack",
				"--set-string recipe=x",
				"--set-string profileName=gb10-x2",
				"--set-string 'nodes[0]=<node-1>'",
				"--set-string 'nodes[1]=<node-2>'",
				"--set-string 'runtimeClassName=<runtime-class>'",
				"--set-string 'storageClassName=<storage-class>'",
				"--set-string sharedCAConfigMap=llm-gateway-stack-ca",
				"--wait --timeout 120m",
			].join(" \\\n  "),
		);
	});

	it("adds node capabilities for NVMe offload", () => {
		const nvme = profile("nvme", {
			perNode: [
				{
					rank: 0,
					role: "model",
					resources: { memoryRequestBytes: GiB },
					storage: { offloadMedium: "local-nvme" },
				},
			],
		});
		nvme.deployment.requiredSiteValues.push("nodeCapabilities");
		const [m] = groupRecipes([recipe("flash", { profiles: [nvme] })]);
		if (!m) throw new Error("no model");
		const [config] = configurations(m);
		if (!config) throw new Error("no configuration");

		expect(nodeCapabilities(config)).toEqual({
			"<node-1>": { localNvme: true },
		});
		expect(helmInstallCommand(config)).toContain(
			`--set-json 'nodeCapabilities={"<node-1>":{"localNvme":true}}'`,
		);
	});

	it("adds each node's fabric address for a multi-node fabric", () => {
		const tp2 = profile("tp2", { nodes: 2, fabric: { minimumGbps: 200 } });
		tp2.deployment.requiredSiteValues.push("nodeCapabilities");
		const [m] = groupRecipes([recipe("flash", { profiles: [tp2] })]);
		if (!m) throw new Error("no model");
		const [config] = configurations(m);
		if (!config) throw new Error("no configuration");

		expect(nodeCapabilities(config)).toEqual({
			"<node-1>": {
				fabric: "<fabric-name>",
				address: "<node-1-fabric-address>",
				interface: "<fabric-interface>",
				linkGbps: "<link-gbps>",
			},
			"<node-2>": {
				fabric: "<fabric-name>",
				address: "<node-2-fabric-address>",
				interface: "<fabric-interface>",
				linkGbps: "<link-gbps>",
			},
		});
		expect(helmInstallCommand(config)).toContain(
			`"<node-2>":{"fabric":"<fabric-name>","address":"<node-2-fabric-address>"`,
		);
	});

	it("escapes a single quote for the shell", () => {
		const quoted = profile("quoted");
		quoted.deployment.values.profileName = "it's";
		const [m] = groupRecipes([recipe("r", { profiles: [quoted] })]);
		if (!m) throw new Error("no model");
		const [config] = configurations(m);
		if (!config) throw new Error("no configuration");
		expect(helmInstallCommand(config)).toContain(
			`--set-string 'profileName=it'\\''s'`,
		);
	});
});

describe("search validators", () => {
	it("keeps a query and a known hardware class", () => {
		expect(validateRecipesSearch({ q: "qwen", hardware: "DGX Spark" })).toEqual(
			{
				q: "qwen",
				hardware: "DGX Spark",
			},
		);
		expect(validateRecipeSearch({ config: "qwen3.8-27b/spark-fp8" })).toEqual({
			config: "qwen3.8-27b/spark-fp8",
		});
	});

	it("overrides invalid values with undefined, so the raw ones don't show through", () => {
		expect(validateRecipesSearch({ q: "", hardware: "Laptop" })).toEqual({
			q: undefined,
			hardware: undefined,
		});
		expect(validateRecipeSearch({ config: 3 })).toEqual({ config: undefined });
	});
});
