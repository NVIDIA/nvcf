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
	Panel,
	StatusMessage,
	type TabItem,
	Tabs,
} from "@nvidia/foundations-react-core";
import { Lightbulb } from "lucide-react";
import { DetailList } from "~/components/DetailList";
import type { GetClusterResponseGpuUsage } from "~/generated/model/getClusterResponseGpuUsage";
import type { GpuResponseSchema } from "~/generated/model/gpuResponseSchema";

export function InstanceConfigPanel({
	gpus,
	usage,
}: {
	gpus: GpuResponseSchema[];
	usage: GetClusterResponseGpuUsage;
}) {
	const gpuItems: TabItem[] = gpus.map((gpu) => {
		const instanceTypes = gpu.instanceTypes ?? [];

		const instanceItems: TabItem[] = instanceTypes.map((instance) => ({
			value: instance.name,
			children: instance.name,
			slotContent: (
				<DetailList
					items={[
						{ label: "Value", value: instance.value },
						{
							label: "Default",
							value:
								instance.default != null
									? instance.default
										? "Yes"
										: "No"
									: undefined,
						},
						{ label: "System Memory", value: instance.systemMemory },
						{ label: "GPU Count", value: instance.gpuCount },
						{ label: "Description", value: instance.description },
						{ label: "CPU Cores", value: instance.cpuCores },
						{ label: "Total GPU Memory", value: instance.gpuMemory },
					]}
				/>
			),
		}));

		return {
			value: gpu.name,
			children: gpu.name,
			slotContent: (
				<div className="flex w-full flex-col gap-4">
					<DetailList
						items={[
							{ label: "Capacity", value: usage?.[gpu.name]?.capacity },
							{ label: "Instance Types", value: instanceTypes.length },
						]}
					/>
					{instanceItems.length > 0 && (
						<Tabs
							className="w-full"
							defaultValue={instanceTypes[0].name}
							items={instanceItems}
							kind="secondary"
						/>
					)}
				</div>
			),
		};
	});

	return (
		<Panel elevation="high" slotHeading="Instance Configuration">
			{gpuItems.length === 0 ? (
				<StatusMessage
					slotHeading="No Instance Configuration Yet"
					slotMedia={<Lightbulb size={32} />}
					slotSubheading="Instance configuration will appear once the cluster is ready"
				/>
			) : (
				<Tabs
					className="w-full overflow-hidden"
					defaultValue={gpus[0].name}
					items={gpuItems}
				/>
			)}
		</Panel>
	);
}
