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

import {
	asHardwareClass,
	HARDWARE_CLASSES,
	type HardwareClass,
	hardwareClass,
} from "~/features/hardware/hardware";
import type { Recipe } from "~/generated/model/recipe";
import type { RecipeChart } from "~/generated/model/recipeChart";
import type { RecipeProfile } from "~/generated/model/recipeProfile";
import type { RecipeValidationRun } from "~/generated/model/recipeValidationRun";
import { RegistryHealth } from "~/generated/model/registryHealth";

/**
 * The shared stack's namespace. Its operator watches only that namespace, so
 * every recipe installs there.
 */
export const RECIPE_NAMESPACE = "llm-stack";

/** The CA ConfigMap the shared stack creates, which recipes trust. */
export const SHARED_CA_CONFIGMAP = "llm-gateway-stack-ca";

/** Helm's wait for a recipe: first installs download tens of GiB of weights. */
export const INSTALL_TIMEOUT = "120m";

/**
 * One model on the recipes page: every build in the catalog with the same
 * name, such as Qwen3.8-27B's FP8 and NVFP4 builds (the mocks: one card per
 * model, not per build).
 */
export interface RecipeModel {
	/** The model's URL id: its name, lowercased, other characters as `-`. */
	slug: string;
	name: string;
	description: string | undefined;
	/** Its builds, in catalog order. */
	builds: Recipe[];
	/** Systems some build runs on, in {@link HARDWARE_CLASSES} order. */
	hardware: HardwareClass[];
	/** Whether some build can be deployed. */
	deployable: boolean;
}

/** One build on one hardware configuration: what a deployment installs. */
export interface RecipeConfiguration {
	/** `build/profile`, the detail page's `?config=`. */
	key: string;
	build: Recipe;
	profile: RecipeProfile;
	hardware: HardwareClass | undefined;
	nodeCount: number;
	/** The most any one node reserves for the model. */
	memoryPerNodeBytes: number | undefined;
}

export function recipeSlug(name: string): string {
	return name
		.trim()
		.toLowerCase()
		.replace(/[^a-z0-9.]+/g, "-")
		.replace(/^-+|-+$/g, "");
}

function profileHardware(profile: RecipeProfile): HardwareClass | undefined {
	for (const product of profile.hardware.gpuProducts) {
		const found = hardwareClass(product);
		if (found) return found;
	}
	return undefined;
}

/**
 * The catalog as models, deployable ones first; otherwise in catalog order.
 * Builds group by slug, so names differing only in case or punctuation are
 * one model.
 */
export function groupRecipes(recipes: readonly Recipe[]): RecipeModel[] {
	const models = new Map<string, RecipeModel>();
	for (const recipe of recipes) {
		const slug = recipeSlug(recipe.name) || recipeSlug(recipe.id);
		let model = models.get(slug);
		if (!model) {
			model = {
				slug,
				name: recipe.name,
				description: undefined,
				builds: [],
				hardware: [],
				deployable: false,
			};
			models.set(slug, model);
		}
		model.builds.push(recipe);
		model.description ??= recipe.description;
		model.deployable ||= recipe.availability.deployable;
	}
	for (const model of models.values()) {
		const supported = new Set(
			model.builds.flatMap((build) => build.profiles.map(profileHardware)),
		);
		model.hardware = HARDWARE_CLASSES.filter((h) => supported.has(h));
	}
	return [...models.values()].sort(
		(a, b) => Number(b.deployable) - Number(a.deployable),
	);
}

/** The configurations a model can be deployed in: deployable builds × profiles. */
export function configurations(model: RecipeModel): RecipeConfiguration[] {
	return model.builds
		.filter((build) => build.availability.deployable)
		.flatMap((build) =>
			build.profiles.map((profile) => {
				const memory = profile.perNode.map(
					(node) => node.resources.memoryRequestBytes,
				);
				return {
					key: `${build.id}/${profile.id}`,
					build,
					profile,
					hardware: profileHardware(profile),
					nodeCount: profile.modelNodeCount,
					memoryPerNodeBytes:
						memory.length > 0 ? Math.max(...memory) : undefined,
				};
			}),
		);
}

/** Search by name, description, catalog id, served model name or precision. */
export function matchesSearch(model: RecipeModel, query: string): boolean {
	const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
	if (terms.length === 0) return true;
	const text = [
		model.name,
		model.description,
		...model.builds.flatMap((b) => [b.id, b.servedModelId, b.precision]),
	]
		.filter(Boolean)
		.join(" ")
		.toLowerCase();
	return terms.every((term) => text.includes(term));
}

export function matchesHardware(
	model: RecipeModel,
	hardware: HardwareClass | undefined,
): boolean {
	return hardware === undefined || model.hardware.includes(hardware);
}

/** `104 GiB`: memory sizes as the recipe charts state them, in GiB. */
export function formatGiB(bytes: number): string {
	return `${Math.round(bytes / 2 ** 30)} GiB`;
}

/** The validation run to report: the one-step Helm install's, else the runtime's. */
export function lastValidation(
	profile: RecipeProfile,
): RecipeValidationRun | undefined {
	return (
		profile.validation.automaticHelmWorkload ??
		profile.validation.workload ??
		undefined
	);
}

/** Where `helm` and Flux pull the chart from; a placeholder until it's published. */
export function chartReference(chart: RecipeChart): string {
	const repository =
		chart.repository?.replace(/\/+$/, "") ?? "oci://<chart-registry>";
	return `${repository}/${chart.name}`;
}

/** The release name the recipe charts' planner gives a build by default. */
export function releaseName(build: Recipe): string {
	return build.id.replaceAll(".", "-");
}

function nodePlaceholders(config: RecipeConfiguration): string[] {
	return Array.from({ length: config.nodeCount }, (_, i) => `<node-${i + 1}>`);
}

/**
 * The per-node facts the chart needs for NVMe offload or a node-to-node
 * fabric, keyed by node, with placeholders to fill in; undefined when the
 * profile needs none.
 */
export function nodeCapabilities(
	config: RecipeConfiguration,
): Record<string, Record<string, string | boolean>> | undefined {
	const { profile } = config;
	if (!profile.deployment.requiredSiteValues.includes("nodeCapabilities")) {
		return undefined;
	}
	const nvme = profile.perNode.some(
		(node) => node.storage?.offloadMedium === "local-nvme",
	);
	const fabric = profile.fabric != null;
	return Object.fromEntries(
		nodePlaceholders(config).map((node, i) => [
			node,
			{
				...(nvme ? { localNvme: true } : {}),
				...(fabric
					? {
							fabric: "<fabric-name>",
							address: `<node-${i + 1}-fabric-address>`,
							interface: "<fabric-interface>",
							linkGbps: "<link-gbps>",
						}
					: {}),
			},
		]),
	);
}

/** Quotes an argument for a POSIX shell unless it is plainly safe. */
function shellArgument(value: string): string {
	return /^[\w.,:=/@%+-]+$/.test(value)
		? value
		: `'${value.replaceAll("'", `'\\''`)}'`;
}

/**
 * The `helm install` for a configuration, as the recipe charts' planner
 * (`llm.py`) writes it, with placeholders for the cluster's own values.
 */
export function helmInstallCommand(config: RecipeConfiguration): string {
	const { build, profile } = config;
	const { chart, values } = profile.deployment;
	const settings: [string, string][] = [
		["recipe", values.recipe],
		["profileName", values.profileName],
		...nodePlaceholders(config).map((node, i): [string, string] => [
			`nodes[${i}]`,
			node,
		]),
		["runtimeClassName", "<runtime-class>"],
		["storageClassName", "<storage-class>"],
		["sharedCAConfigMap", SHARED_CA_CONFIGMAP],
	];
	const capabilities = nodeCapabilities(config);
	const lines = [
		`helm install ${releaseName(build)} ${chartReference(chart)}`,
		`--version ${shellArgument(chart.version)}`,
		`--namespace ${RECIPE_NAMESPACE}`,
		...settings.map(
			([key, value]) => `--set-string ${shellArgument(`${key}=${value}`)}`,
		),
		...(capabilities
			? [
					`--set-json ${shellArgument(`nodeCapabilities=${JSON.stringify(capabilities)}`)}`,
				]
			: []),
		`--wait --timeout ${INSTALL_TIMEOUT}`,
	];
	return lines.join(" \\\n  ");
}

/** The recipes list's URL state: the search text and the hardware filter. */
export interface RecipesSearch {
	q?: string | undefined;
	hardware?: HardwareClass | undefined;
}

function nonEmpty(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

// The router merges a route's validated search over the raw URL params, so
// the validators return invalid values as undefined rather than leaving them
// out: left out, the raw value would show through.

/** `validateSearch` for `/recipes`: drops an empty query and unknown hardware. */
export function validateRecipesSearch(
	search: Record<string, unknown>,
): RecipesSearch {
	return { q: nonEmpty(search.q), hardware: asHardwareClass(search.hardware) };
}

/** `validateSearch` for a recipe: `?config=build/profile`, the picked configuration. */
export function validateRecipeSearch(search: Record<string, unknown>): {
	config?: string | undefined;
} {
	return { config: nonEmpty(search.config) };
}

/** A model's build registered with the gateway: its served name and health. */
export interface Deployment {
	model: string;
	health: RegistryHealth;
}

/**
 * The model's builds that are registered with the gateway, healthy ones
 * first, from `registered` (served model name to health).
 */
export function deploymentsOf(
	model: RecipeModel,
	registered: ReadonlyMap<string, RegistryHealth>,
): Deployment[] {
	return model.builds
		.flatMap((build) => {
			const health =
				build.servedModelId == null
					? undefined
					: registered.get(build.servedModelId);
			return build.servedModelId != null && health !== undefined
				? [{ model: build.servedModelId, health }]
				: [];
		})
		.sort(
			(a, b) =>
				Number(b.health === RegistryHealth.Healthy) -
				Number(a.health === RegistryHealth.Healthy),
		);
}

/**
 * How far a configuration was validated, as a badge: the one-step Helm
 * install's outcome when there is one (that is what the snippet runs), else
 * the runtime's. A failed run shows no badge (decided 2026-10-08), and
 * unknown statuses read as themselves.
 */
export function validationLabel(
	profile: RecipeProfile,
): { label: string; color: "green" | "gray" } | undefined {
	const status =
		profile.validation.automaticHelmStatus ?? profile.validation.runtimeStatus;
	switch (status) {
		case "smoke-tested":
		case "hardware-validated":
			return { label: "Tested", color: "green" };
		case "failed":
			return undefined;
		case "pending":
			return { label: "Not tested yet", color: "gray" };
		default:
			return { label: status, color: "gray" };
	}
}
