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

import { faker } from "@faker-js/faker";
import { defineConfig, type OutputOptions } from "orval";

const sharedOutput: OutputOptions = {
	baseUrl: "",
	mode: "tags-split",
	target: "./src/generated/api",
	schemas: {
		path: "./src/generated/model",
		type: "typescript",
	},
	mock: {
		baseUrl: "",
		type: "msw",
		locale: "en",
		useExamples: true,
	},
	client: "react-query",
	httpClient: "fetch",
	indexFiles: false,
	formatter: "biome",
	override: {
		mutator: {
			path: "./src/lib/fetch.ts",
			name: "customFetch",
		},
		fetch: {
			includeHttpResponseReturnType: false,
		},
		mock: {
			delay: 150,
			arrayMin: 1,
			arrayMax: 25,
			numberMin: 0,
			numberMax: 100,
		},
		query: {
			useSuspenseQuery: true,
		},
	},
};

export default defineConfig({
	"control-plane": {
		input: {
			target: "../spec/control-plane-openapi.yaml",
		},
		output: { ...sharedOutput, clean: true },
	},
	nvcf: {
		input: {
			target: "../spec/nvcf-openapi.yaml",
		},
		output: {
			...sharedOutput,
			override: {
				...sharedOutput.override,
				operations: {
					getCloudAccounts: {
						query: {
							options: {
								staleTime: Number.POSITIVE_INFINITY,
							},
						},
					},
				},
				tags: {
					Account: {
						mock: {
							properties: {
								"/cloudAccounts/": () =>
									Array.from(
										{ length: faker.number.int({ min: 1, max: 3 }) },
										() => ({
											ncaId: faker.string.alphanumeric({ length: 13 }),
											name: faker.company.name(),
											maxFunctionsAllowed: faker.number.int(),
											maxTasksAllowed: faker.number.int(),
											maxTelemetriesAllowed: faker.number.int(),
											maxRegistryCredentialsAllowed: faker.number.int(),
										}),
									),
							},
						},
					},
					"Function Management": {
						mock: {
							properties: {
								"/name/": () => faker.lorem.slug(),
								"/tags/": () =>
									faker.helpers.arrayElements(["inference", "gpu", "llm"], {
										min: 0,
										max: 3,
									}),
								"/containerImage/": () =>
									`nvcr.io/nvidia/${faker.lorem.slug()}:${faker.system.semver()}`,
								"/helmChart$/": () =>
									`helm.ngc.nvidia.com/nvidia/${faker.lorem.slug()}`,
								"/inferenceUrl/": () =>
									`https://api.nvcf.nvidia.com/v2/nvcf/pexec/functions/${faker.string.uuid()}`,
								"/health\\.uri/": () =>
									faker.helpers.arrayElement([
										"health/ready",
										"healthz",
										"v1/health/ready",
									]),
								"/uri/": () =>
									`api.ngc.nvidia.com/v2/org/nvidia/models/${faker.lorem.slug()}/versions/${faker.system.semver()}`,
								"/containerEnvironment/": () =>
									faker.helpers.maybe(
										() =>
											Array.from(
												{ length: faker.number.int({ min: 1, max: 3 }) },
												() => ({
													key: `${faker.lorem.word().toUpperCase()}_${faker.lorem.word().toUpperCase()}`,
													value: faker.string.alphanumeric(16),
												}),
											),
										{ probability: 0.7 },
									),
								"/models/": () =>
									faker.helpers.maybe(
										() =>
											Array.from(
												{ length: faker.number.int({ min: 1, max: 3 }) },
												() => ({
													name: faker.lorem.slug(),
													uri: `api.ngc.nvidia.com/v2/org/nvidia/models/${faker.lorem.slug()}/versions/${faker.system.semver()}`,
												}),
											),
										{ probability: 0.7 },
									),
								"/resources/": () =>
									faker.helpers.maybe(
										() =>
											Array.from(
												{ length: faker.number.int({ min: 1, max: 3 }) },
												() => ({
													name: faker.lorem.slug(),
													version: faker.system.semver(),
													uri: `api.ngc.nvidia.com/v2/org/nvidia/resources/${faker.lorem.slug()}/versions/${faker.system.semver()}`,
												}),
											),
										{ probability: 0.7 },
									),
							},
						},
					},
					"Function Deployment": {
						mock: {
							properties: {
								// Build the whole spec array so instance types are unique and
								// each `gpu` matches its instanceType prefix — the raw per-field
								// faker picked gpu/instanceType independently, producing dupes
								// and mismatched pairs.
								"/deploymentSpecifications$/": () => {
									const gpus = [
										{ gpu: "A100", mem: "80GB", gpuMemory: "80Gi" },
										{ gpu: "A10G", mem: "24GB", gpuMemory: "24Gi" },
										{ gpu: "H100", mem: "80GB", gpuMemory: "80Gi" },
										{ gpu: "L40S", mem: "48GB", gpuMemory: "48Gi" },
									];
									const combos = gpus.flatMap((g) =>
										["1x", "2x", "4x", "8x"].map((count) => ({ ...g, count })),
									);
									return faker.helpers
										.arrayElements(combos, faker.number.int({ min: 1, max: 5 }))
										.map((c) => ({
											gpu: c.gpu,
											instanceType: `${c.gpu}.${c.mem}.${c.count}`,
											gpuMemory: c.gpuMemory,
											systemMemory: faker.helpers.arrayElement([
												"256Gi",
												"512Gi",
												"1024Gi",
											]),
											storage: faker.helpers.arrayElement([
												"512Gi",
												"1Ti",
												"2Ti",
											]),
											minInstances: faker.number.int({ min: 0, max: 2 }),
											maxInstances: faker.number.int({ min: 3, max: 16 }),
											maxRequestConcurrency: faker.helpers.arrayElement([
												1, 4, 8, 16,
											]),
											regions: faker.helpers.arrayElements(
												[
													"us-east-1",
													"us-west-2",
													"eu-central-1",
													"ap-southeast-1",
												],
												{ min: 1, max: 2 },
											),
											clusters: faker.helpers.arrayElements(
												["prod-cluster-a", "prod-cluster-b", "prod-cluster-c"],
												{ min: 1, max: 2 },
											),
										}));
								},
								"/backend/": () =>
									faker.helpers.arrayElement(["AWS", "GCP", "AZURE"]),
								"/location/": () =>
									faker.helpers.arrayElement([
										"us-east-1",
										"us-west-2",
										"eu-central-1",
										"ap-southeast-1",
									]),
							},
						},
					},
				},
			},
		},
	},
	sis: {
		input: {
			target: "../spec/sis-openapi.yaml",
		},
		output: {
			...sharedOutput,
			override: {
				...sharedOutput.override,
				tags: {
					Clusters: {
						mock: {
							properties: {
								"/clusterId/": () => faker.string.uuid(),
								"/clusterGroupId/": () => faker.string.uuid(),
								"/clusterKeyId/": () => faker.string.uuid(),
								"/clusterName/": () => {
									const user = faker.internet.username().toLowerCase();
									return `${user}-cluster`;
								},
								"/clusterGroupName/": () => {
									const user = faker.internet.username().toLowerCase();
									return `${user}-cluster`;
								},
								"/region/": () =>
									faker.helpers.arrayElement([
										"us-east-1",
										"us-west-1",
										"us-west-2",
										"eu-central-1",
										"ap-south-1",
										"ap-northeast-1",
									]),
								"/k8sVersion/": () =>
									`v1.${faker.number.int({ min: 29, max: 34 })}.${faker.number.int({ min: 0, max: 9 })}`,
								"/nvcaVersion/": () =>
									`3.0.${faker.number.int({ min: 0, max: 5 })}`,
								"/driverVersion/": () =>
									faker.helpers.arrayElement([
										"555.42.06",
										"560.28.03",
										"565.57.01",
									]),
								// Build the gpus array so each GPU is unique and its instance
								// types are unique + coherent (name = gpu.mem.count). The raw
								// per-field faker duped GPU names and instance-type names.
								"/gpus$/": () => {
									const gpuDefs = [
										{ name: "A100", mem: "80GB", gpuMemory: "80Gi" },
										{ name: "A10G", mem: "24GB", gpuMemory: "24Gi" },
										{ name: "H100", mem: "80GB", gpuMemory: "80Gi" },
										{ name: "L40S", mem: "48GB", gpuMemory: "48Gi" },
										{ name: "H200", mem: "141GB", gpuMemory: "141Gi" },
									];
									return faker.helpers
										.arrayElements(
											gpuDefs,
											faker.number.int({ min: 1, max: 3 }),
										)
										.map((g) => ({
											name: g.name,
											capacity: faker.helpers.arrayElement([8, 16, 32, 64]),
											instanceTypes: faker.helpers
												.arrayElements(
													["1x", "2x", "4x", "8x"],
													faker.number.int({ min: 1, max: 3 }),
												)
												.map((count) => ({
													name: `${g.name}.${g.mem}.${count}`,
													value: `${g.name}.${g.mem}.${count}`,
													gpuCount: Number(count.replace("x", "")),
													gpuMemory: g.gpuMemory,
													systemMemory: faker.helpers.arrayElement([
														"256Gi",
														"512Gi",
														"1024Gi",
													]),
													cpuCores: faker.helpers.arrayElement([
														16, 32, 64, 128,
													]),
													default: false,
												})),
										}));
								},
								"/gpuUsage/": () => {
									const gpuName = faker.helpers.arrayElement([
										"H100",
										"A100",
										"L40S",
										"H200",
									]);
									const capacity = faker.helpers.arrayElement([8, 16, 32, 64]);
									const allocated = faker.number.int({
										min: 0,
										max: capacity,
									});
									return {
										[gpuName]: {
											capacity,
											allocated,
											available: capacity - allocated,
										},
									};
								},
								"/gpuCount/": () =>
									faker.helpers.arrayElement([1, 2, 4, 8, 16]),
								"/gpuMemory/": () =>
									faker.helpers.arrayElement([
										"79Gi",
										"159Gi",
										"318Gi",
										"637Gi",
										"1274Gi",
									]),
								"/systemMemory/": () =>
									faker.helpers.arrayElement([
										"8034Mi",
										"15Gi",
										"31Gi",
										"62Gi",
										"125Gi",
									]),
								"/storage/": () =>
									faker.helpers.arrayElement([
										"104Gi",
										"250Gi",
										"455Gi",
										"911Gi",
										"1823Gi",
									]),
							},
						},
					},
				},
			},
		},
	},
});
