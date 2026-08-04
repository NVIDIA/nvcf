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
	Breadcrumbs,
	PageHeader,
	Panel,
	StatusMessage,
	type TabItem,
	Tabs,
} from "@nvidia/foundations-react-core";
import { createLazyRoute, Link } from "@tanstack/react-router";
import { Lightbulb } from "lucide-react";
import { DetailList } from "~/components/DetailList";
import { useGetClusterForNcaIdAndClusterIdSuspense } from "~/generated/api/clusters/clusters";
import type { GetClusterResponseGpuUsage } from "~/generated/model/getClusterResponseGpuUsage";
import type { GpuResponseSchema } from "~/generated/model/gpuResponseSchema";
import { DETAIL_REFETCH_INTERVAL } from "~/lib/queryClient";
import { formatDateTime } from "~/utils/formatters";
import { GpuUtilization } from "./components/GpuUtilization";
import { StatusBadge } from "./components/StatusBadge";
import { clusterDetailRoute } from "./routes";

export const ClusterDetailsRoute = createLazyRoute("/clusters/$clusterId")({
	component: ClusterDetails,
});

function InstanceConfigPanel({
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

function ClusterDetails() {
	const { ncaId } = clusterDetailRoute.useLoaderData();
	const { clusterId } = clusterDetailRoute.useParams();

	const { data: cluster } = useGetClusterForNcaIdAndClusterIdSuspense(
		ncaId,
		clusterId,
		{ query: { refetchInterval: DETAIL_REFETCH_INTERVAL } },
	);

	const cfg = cluster.clusterConfigurations ?? {};

	return (
		<div className="flex flex-col gap-6">
			<PageHeader
				kind="flat"
				slotBreadcrumbs={
					<Breadcrumbs
						items={[
							{
								children: (
									<Link to="/clusters" viewTransition>
										Compute Clusters
									</Link>
								),
							},
							cluster.clusterName ?? clusterId,
						]}
					/>
				}
				slotHeading={cluster.clusterName ?? clusterId}
			/>
			<div className="grid grid-cols-1 gap-6 lg:grid-cols-2 lg:gap-x-8 lg:items-start">
				<div className="flex flex-col gap-6">
					<Panel elevation="high" slotHeading="Overview">
						<div className="flex flex-col gap-6">
							<DetailList
								items={[
									{
										label: "Status",
										value: <StatusBadge status={cluster.status} />,
									},
									{
										label: "Cluster Description",
										value: cluster.clusterDescription,
									},
									{
										label: "Cluster Agent Version",
										value: cluster.nvcaVersion,
									},
									{ label: "Cluster Group", value: cluster.clusterGroupName },
									{ label: "Region", value: cluster.region },
									{
										label: "Last Updated",
										value: formatDateTime(cluster.nvcaLastConnected),
									},
								]}
							/>
							{cluster.gpuUsage && Object.keys(cluster.gpuUsage).length > 0 && (
								<DetailList
									items={[
										{
											label: "GPU Utilization",
											value: (
												<div className="flex flex-col gap-6">
													{Object.entries(cluster.gpuUsage ?? {}).map(
														([name, usage]) => (
															<GpuUtilization
																key={name}
																name={name}
																usage={usage}
															/>
														),
													)}
												</div>
											),
										},
									]}
								/>
							)}
						</div>
					</Panel>
					<InstanceConfigPanel
						gpus={cluster.gpus ?? []}
						usage={cluster.gpuUsage || {}}
					/>
				</div>

				<Panel elevation="high" slotHeading="Configuration">
					<DetailList
						items={[
							{
								label: "Node-selector Label",
								value: cfg["node-selector-label"],
							},
							{ label: "Priority Class", value: cfg["priority-class"] },
							{
								label: "Model Cache Volume Mount Options",
								value: cfg["model-cache-volume-mount-options"],
							},
							{
								label: "Network CIDR Range",
								value: cfg["network-cidr-range"],
							},
							{
								label: "Worker Degradation Period",
								value: cfg["worker-degradation-period"],
							},
							{ label: "Cluster Attributes", value: cluster.attributes },
							{ label: "Cluster Features", value: cluster.capabilities },
						]}
					/>
				</Panel>
			</div>
		</div>
	);
}
