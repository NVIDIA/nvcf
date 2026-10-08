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
		generators: [{ type: "msw", baseUrl: "", locale: "en", useExamples: true }],
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
			arrayMax: 8,
		},
		query: {
			useSuspenseQuery: true,
		},
		// Orval 8.9 imports a response component `X` from `model/x` but writes it
		// as `model/xResponse.ts`. Without a suffix the two agree, so response
		// components are named with their suffix in the specs instead.
		components: {
			responses: { suffix: "" },
		},
	},
};

export default defineConfig({
	"llm-gateway": {
		input: {
			target: "../spec/llm-gateway-openapi.yaml",
		},
		output: { ...sharedOutput, clean: true },
	},
	bff: {
		input: {
			target: "../spec/bff-openapi.yaml",
		},
		output: {
			...sharedOutput,
			override: {
				...sharedOutput.override,
				operations: {
					// Deployment settings only change on a redeploy, which reloads the page.
					getConfig: {
						query: {
							options: {
								staleTime: Number.POSITIVE_INFINITY,
							},
						},
					},
				},
			},
		},
	},
});
