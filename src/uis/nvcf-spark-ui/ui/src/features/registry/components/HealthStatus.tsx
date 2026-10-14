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

import { StatusIndicator, Text } from "@nvidia/foundations-react-core";
import { RegistryHealth } from "~/generated/model/registryHealth";

/** A registry health value as a colored dot plus its label. */
export function HealthStatus({ health }: { health: RegistryHealth }) {
	return (
		<span className="inline-flex items-center gap-2">
			<StatusIndicator
				color={health === RegistryHealth.Healthy ? "green" : "red"}
				size="small"
			/>
			<Text kind="label/regular/md">{health}</Text>
		</span>
	);
}
