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

import { Badge, Text } from "@nvidia/foundations-react-core";
import {
	formatGiB,
	lastValidation,
	type RecipeConfiguration,
	validationLabel,
} from "../utils";

/**
 * One hardware configuration to pick (slide 8): the system and node count,
 * then the build's precision, memory and context, then how far it was tested.
 */
export function ConfigurationTile({ config }: { config: RecipeConfiguration }) {
	const { build, profile, memoryPerNodeBytes } = config;
	const system =
		config.hardware ?? profile.hardware.gpuProducts[0] ?? "Unknown hardware";
	const validation = validationLabel(profile);
	const run = lastValidation(profile);
	const context = profile.workload?.maximumContextTokens;
	return (
		<div className="flex min-w-0 flex-col gap-1">
			<Text kind="label/semibold/md">
				{system} × {config.nodeCount}
			</Text>
			<Text className="text-secondary" kind="body/regular/sm">
				{[
					build.precision,
					memoryPerNodeBytes === undefined
						? undefined
						: `${formatGiB(memoryPerNodeBytes)} per node`,
					context === undefined
						? undefined
						: `up to ${context.toLocaleString("en-US")} tokens`,
				]
					.filter(Boolean)
					.join(" · ")}
			</Text>
			{validation ? (
				<div className="flex flex-wrap items-center gap-2 pt-1">
					<Badge color={validation.color} kind="solid">
						{validation.label}
					</Badge>
					{run?.date ? (
						<Text className="text-secondary" kind="body/regular/sm">
							on {run.date}
						</Text>
					) : null}
				</div>
			) : null}
		</div>
	);
}
