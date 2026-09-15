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
	Button,
	Card,
	FormField,
	Pagination,
	Skeleton,
	StatusMessage,
	Text,
} from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import { AlertTriangleIcon, CircleAlertIcon } from "lucide-react";
import { useState } from "react";
import { AsyncBoundary } from "~/components/AsyncBoundary";
import { GpuUtilization } from "~/features/clusters/components/GpuUtilization";
import { useGetClustersSuspense } from "~/generated/api/clusters/clusters";
import type { GetClusterResponse } from "~/generated/model/getClusterResponse";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { StatusBadge } from "../../clusters/components/StatusBadge";

const PAGE_SIZE = 2;

const readyClustersSelect = (clusters: GetClusterResponse[]) =>
	clusters
		.filter((c) => c.status === GetClusterResponseStatus.READY)
		.slice(0, 3);

function ClusterCard({ cluster }: { cluster: GetClusterResponse }) {
	const [gpuPage, setGpuPage] = useState(1);
	const gpuEntries = Object.entries(cluster.gpuUsage ?? {});
	const visibleGpus = gpuEntries.slice(
		(gpuPage - 1) * PAGE_SIZE,
		gpuPage * PAGE_SIZE,
	);

	return (
		<Card
			className="relative"
			interactive
			slotHeader={<Text kind="label/bold/lg">{cluster.clusterName}</Text>}
		>
			{/* Stretched overlay link: the whole card navigates, but the content
			    (incl. the interactive GPU pagination) is a DOM sibling of the anchor,
			    not nested inside it — so pagination stays clickable and it's valid,
			    accessible HTML. */}
			<Link
				aria-label={`View ${cluster.clusterName ?? cluster.clusterId} details`}
				className="absolute inset-0 rounded-[inherit]"
				params={{ clusterId: cluster.clusterId || "" }}
				to="/clusters/$clusterId"
				viewTransition
			/>
			<div className="grid grid-cols-[88px_1fr] gap-6">
				<div className="flex flex-col gap-6">
					<FormField slotLabel="Status">
						<StatusBadge status={cluster.status} />
					</FormField>
					<FormField slotLabel="Region">
						<Text kind="label/regular/md">{cluster.region}</Text>
					</FormField>
				</div>
				<div className="flex flex-col gap-4">
					<div className="grid grid-rows-2 gap-4">
						{visibleGpus.map(([name, usage]) => (
							<GpuUtilization key={name} name={name} usage={usage} />
						))}
					</div>
					{gpuEntries.length > PAGE_SIZE && (
						// `relative` lifts the pagination above the overlay link so its
						// tabs remain clickable and don't trigger navigation.
						<div className="relative">
							<Pagination
								displayControls={false}
								kind="tabs"
								onPageChange={setGpuPage}
								page={gpuPage}
								pageSize={PAGE_SIZE}
								renderedItemCount={5}
								totalItems={gpuEntries.length}
							/>
						</div>
					)}
				</div>
			</div>
		</Card>
	);
}

function ClusterCardSkeleton() {
	return (
		<Card slotHeader={<Skeleton className="h-[18px] w-36" kind="line" />}>
			<div className="grid grid-cols-[88px_1fr] gap-6">
				<div className="flex flex-col gap-6">
					{/* Status FormField: label + badge */}
					<div className="flex flex-col gap-1">
						<Skeleton className="h-[19px] w-10" kind="line" />
						<Skeleton className="h-[18px] w-14" kind="line" />
					</div>
					{/* Region FormField: label + text */}
					<div className="flex flex-col gap-1">
						<Skeleton className="h-[19px] w-12" kind="line" />
						<Skeleton className="h-[17px] w-20" kind="line" />
					</div>
				</div>
				<div className="grid grid-rows-2 gap-4">
					{/* One GPU FormField: label (name + pct side by side) / progress bar / help text */}
					<div className="flex flex-col gap-1">
						<div className="flex items-center justify-between">
							<Skeleton className="h-[19px] w-24" kind="line" />
							<Skeleton className="h-[19px] w-8" kind="line" />
						</div>
						<Skeleton className="h-3 w-full" kind="line" />
						<Skeleton className="h-3 w-16" kind="line" />
					</div>
				</div>
			</div>
		</Card>
	);
}

export function ReadyClusterCards({ ncaId }: { ncaId: string }) {
	return (
		<AsyncBoundary
			errorFallback={({ resetErrorBoundary }) => (
				<StatusMessage
					slotFooter={
						<Button kind="secondary" onClick={resetErrorBoundary}>
							Retry
						</Button>
					}
					slotHeading="Failed to load clusters."
					slotMedia={<AlertTriangleIcon />}
				/>
			)}
			fallback={
				<div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">
					<ClusterCardSkeleton />
					<ClusterCardSkeleton />
					<ClusterCardSkeleton />
				</div>
			}
		>
			<div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">
				<ReadyClusterCardsContent ncaId={ncaId} />
			</div>
		</AsyncBoundary>
	);
}

function ReadyClusterCardsContent({ ncaId }: { ncaId: string }) {
	const { data: clusters } = useGetClustersSuspense(ncaId, undefined, {
		query: { select: readyClustersSelect },
	});

	if (!clusters.length) {
		return (
			<div className="col-span-full place-content-center min-h-48">
				<StatusMessage
					slotHeading="No ready clusters"
					slotMedia={<CircleAlertIcon size={36} />}
				/>
			</div>
		);
	}

	return clusters.map((cluster) => (
		<ClusterCard cluster={cluster} key={cluster.clusterId} />
	));
}
