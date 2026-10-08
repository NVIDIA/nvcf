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

import { Badge } from "@nvidia/foundations-react-core";
import { RegistryHealth } from "~/generated/model/registryHealth";
import type { Deployment, RecipeModel } from "../utils";

/**
 * A model's state at a glance: deployed (and whether healthy) when a build is
 * registered with the gateway, or not available when no build can be
 * deployed. Nothing otherwise.
 */
export function RecipeStatusBadge({
	model,
	deployments,
}: {
	model: RecipeModel;
	deployments: readonly Deployment[];
}) {
	if (deployments.length > 0) {
		const healthy = deployments.some(
			(d) => d.health === RegistryHealth.Healthy,
		);
		return (
			<Badge color={healthy ? "green" : "yellow"} kind="solid">
				{healthy ? "Deployed" : "Deployed, unhealthy"}
			</Badge>
		);
	}
	if (!model.deployable) {
		return (
			<Badge color="gray" kind="solid">
				Not available
			</Badge>
		);
	}
	return null;
}
