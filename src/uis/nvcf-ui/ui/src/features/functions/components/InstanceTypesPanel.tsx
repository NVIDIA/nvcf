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
	Anchor,
	Button,
	Panel,
	Skeleton,
	StatusMessage,
	type TabItem,
	Tabs,
	Text,
} from "@nvidia/foundations-react-core";
import { AlertTriangleIcon, Lightbulb } from "lucide-react";
import { AsyncBoundary } from "~/components/AsyncBoundary";
import { DetailList } from "~/components/DetailList";
import { useGetFunctionDeploymentSuspense } from "~/generated/api/function-deployment/function-deployment";
import type { GpuSpecificationDto } from "~/generated/model/gpuSpecificationDto";
import { DETAIL_REFETCH_INTERVAL } from "~/lib/queryClient";

interface InstanceTypesPanelProps {
	ncaId: string;
	functionId: string;
	versionId: string;
}

/**
 * Deployment instance types for a function version. Reads the deployment
 * query the route loader starts without awaiting, so loading and errors stay
 * inside this panel.
 */
export function InstanceTypesPanel(props: InstanceTypesPanelProps) {
	return (
		<AsyncBoundary
			errorFallback={({ resetErrorBoundary }) => (
				<Panel elevation="high" slotHeading="Instance Types">
					<StatusMessage
						slotFooter={
							<Button kind="secondary" onClick={resetErrorBoundary}>
								Retry
							</Button>
						}
						slotHeading="Failed to load instance types."
						slotMedia={<AlertTriangleIcon />}
					/>
				</Panel>
			)}
			fallback={<InstanceTypesPanelSkeleton />}
		>
			<InstanceTypesPanelContent {...props} />
		</AsyncBoundary>
	);
}

function InstanceTypesPanelContent({
	ncaId,
	functionId,
	versionId,
}: InstanceTypesPanelProps) {
	const { data } = useGetFunctionDeploymentSuspense(
		ncaId,
		functionId,
		versionId,
		{
			query: { refetchInterval: DETAIL_REFETCH_INTERVAL },
		},
	);
	const specs = data.deployment.deploymentSpecifications;

	if (specs.length === 0) {
		return (
			<Panel elevation="high" slotHeading="Instance Types">
				<StatusMessage
					slotHeading="No Instance Type Yet"
					slotMedia={<Lightbulb size={32} />}
					slotSubheading="Instance types will appear once function is deployed"
				/>
			</Panel>
		);
	}

	const gpuNames = [...new Set(specs.map((s) => s.gpu))];

	const gpuItems: TabItem[] = gpuNames.map((gpu) => {
		const gpuSpecs = specs.filter((s) => s.gpu === gpu);
		const instanceItems: TabItem[] = gpuSpecs.map((spec) => ({
			value: spec.instanceType,
			children: spec.instanceType,
			slotContent: <SpecDetails spec={spec} />,
		}));

		return {
			value: gpu,
			children: gpu,
			slotContent: (
				<Tabs
					className="w-full"
					defaultValue={gpuSpecs[0].instanceType}
					items={instanceItems}
					kind="secondary"
				/>
			),
		};
	});

	return (
		<Panel elevation="high" slotHeading="Instance Types">
			<Tabs
				className="w-full overflow-hidden"
				defaultValue={gpuNames[0]}
				items={gpuItems}
			/>
		</Panel>
	);
}

function InstanceTypesPanelSkeleton() {
	return (
		<Panel elevation="high" slotHeading="Instance Types">
			<div className="flex flex-col gap-4">
				{/* GPU tabs (primary) */}
				<div className="flex gap-2">
					<Skeleton className="h-8 w-16" kind="line" />
					<Skeleton className="h-8 w-16" kind="line" />
					<Skeleton className="h-8 w-16" kind="line" />
				</div>
				{/* Instance type tabs (secondary) */}
				<div className="flex gap-2">
					<Skeleton className="h-6 w-28" kind="pill" />
					<Skeleton className="h-6 w-28" kind="pill" />
				</div>
				{/* Spec detail rows */}
				{Array.from({ length: 6 }).map((_, i) => (
					// biome-ignore lint: skeleton rows have no identity
					<div className="flex items-center gap-4" key={i}>
						<Skeleton className="h-4 w-36 shrink-0" kind="line" />
						<Skeleton className="h-4 w-24" kind="line" />
					</div>
				))}
			</div>
		</Panel>
	);
}

function SpecDetails({ spec }: { spec: GpuSpecificationDto }) {
	return (
		<DetailList
			items={[
				{ label: "RAM", value: spec.systemMemory },
				{ label: "GPUs", value: spec.gpu },
				{ label: "VRAM", value: spec.gpuMemory },
				{ label: "Storage", value: spec.storage },
				{
					label: "Clusters",
					value: spec.clusters?.length ? spec.clusters : spec.backend,
				},
				{ label: "Target Regions", value: spec.regions },
				{ label: "Attributes", value: spec.attributes },
				{
					label: "Min / Max Instances",
					value: `${spec.minInstances} / ${spec.maxInstances}`,
				},
				{
					label: "Max Concurrency",
					value: spec.maxRequestConcurrency,
					tooltip: (
						<div className="w-xs">
							<Text kind="body/regular/md">
								Total maximum concurrency for this function is the combined
								maximum concurrency across versions and their instance types.{" "}
								<br /> <br />
								Max concurrency defines how many requests a single instance can
								handle simultaneously. You can update this total max concurrency
								number by adjusting per instance type concurrency in the
								deployment specification. For more details, please refer to the{" "}
								<Anchor
									href="https://docs.nvidia.com/nvcf/dev/function-lifecycle/#function-creation-management--deployment"
									kind="inline"
									rel="noreferrer"
									target="_blank"
								>
									documentation
								</Anchor>
								.
							</Text>
						</div>
					),
				},
			]}
		/>
	);
}
