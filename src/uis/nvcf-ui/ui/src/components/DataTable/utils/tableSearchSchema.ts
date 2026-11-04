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

import { z } from "zod";

export const baseTableSearchSchema = z.object({
	page: z.number().min(1).default(1).catch(1),
	pageSize: z.number().min(1).default(10).catch(10),
	search: z.string().default("").catch(""),
	sort: z
		.array(z.object({ id: z.string(), desc: z.boolean() }))
		.default([])
		.catch([]),
	filters: z
		.array(
			z.object({
				id: z.string(),
				value: z.union([z.string(), z.array(z.string())]),
			}),
		)
		.default([])
		.catch([]),
});

export type BaseTableSearch = z.infer<typeof baseTableSearchSchema>;
