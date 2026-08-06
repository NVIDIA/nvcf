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

import { FormField, ProgressBar, Text } from "@nvidia/foundations-react-core";
import type { GpuCapacity } from "~/generated/model/gpuCapacity";

type GpuUtilizationProps = {
	name: string;
	usage: GpuCapacity;
};

export function GpuUtilization({ name, usage }: GpuUtilizationProps) {
	const capacity = usage.capacity ?? 0;
	const available = usage.available ?? 0;
	const used = capacity - available;
	const pct = capacity > 0 ? Math.round((used / capacity) * 100) : 0;
	const id = `gpu-util-${name}`;

	return (
		<FormField
			attributes={{ Label: { className: "w-full" } }}
			id={id}
			slotHelp={`${used}/${capacity} GPUs`}
			slotLabel={
				<div className="flex items-center justify-between">
					<span>{name}</span>
					<Text kind="label/regular/sm">{pct}%</Text>
				</div>
			}
		>
			<ProgressBar
				aria-label={name}
				className="[&>div]:bg-blue-300"
				value={pct}
			/>
		</FormField>
	);
}
