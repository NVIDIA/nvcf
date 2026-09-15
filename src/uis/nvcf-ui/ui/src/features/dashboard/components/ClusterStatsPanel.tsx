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

import { Anchor, Panel, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import clsx from "clsx";
import groupBy from "lodash-es/groupBy";
import { BoxesIcon } from "lucide-react";
import { useGetClustersSuspense } from "~/generated/api/clusters/clusters";
import type { GetClusterResponse } from "~/generated/model/getClusterResponse";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { formatNumberCompact } from "~/utils/formatters";
import { getStatusLabel } from "../../clusters/components/StatusBadge";
import { StatPanelBoundary } from "./StatPanelBoundary";

const clustersSelect = (clusters: GetClusterResponse[]) => {
	const grouped = groupBy(clusters, (c) => {
		if (c.status === GetClusterResponseStatus.READY)
			return GetClusterResponseStatus.READY;
		if (c.status === GetClusterResponseStatus.UNHEALTHY)
			return GetClusterResponseStatus.UNHEALTHY;
		return GetClusterResponseStatus.NOT_READY;
	});
	return {
		[GetClusterResponseStatus.READY]:
			grouped[GetClusterResponseStatus.READY]?.length ?? 0,
		[GetClusterResponseStatus.UNHEALTHY]:
			grouped[GetClusterResponseStatus.UNHEALTHY]?.length ?? 0,
		[GetClusterResponseStatus.NOT_READY]:
			grouped[GetClusterResponseStatus.NOT_READY]?.length ?? 0,
	};
};

function ClusterStatsPanelContent({ ncaId }: { ncaId: string }) {
	const { data } = useGetClustersSuspense(ncaId, undefined, {
		query: { select: clustersSelect },
	});

	return (
		<Panel
			elevation="high"
			slotHeading="Compute Clusters"
			slotIcon={<BoxesIcon />}
		>
			<div className="flex flex-wrap items-baseline gap-x-6 gap-y-2">
				<div className="flex items-baseline gap-2">
					<Text className="text-brand" kind="display/md">
						{formatNumberCompact(data[GetClusterResponseStatus.READY])}
					</Text>
					<Anchor asChild>
						<Link
							search={{
								filters: [
									{
										id: "status",
										value: [getStatusLabel(GetClusterResponseStatus.READY)],
									},
								],
							}}
							to="/clusters"
							viewTransition
						>
							Ready
						</Link>
					</Anchor>
				</div>
				<div className="flex items-baseline gap-2">
					<Text
						className={clsx(
							data[GetClusterResponseStatus.UNHEALTHY] > 0 && "text-accent-red",
						)}
						kind="display/md"
					>
						{formatNumberCompact(data[GetClusterResponseStatus.UNHEALTHY])}
					</Text>
					<Anchor asChild>
						<Link
							search={{
								filters: [
									{
										id: "status",
										value: [getStatusLabel(GetClusterResponseStatus.UNHEALTHY)],
									},
								],
							}}
							to="/clusters"
							viewTransition
						>
							Error
						</Link>
					</Anchor>
				</div>
				<div className="flex items-baseline gap-2">
					<Text kind="display/md">
						{formatNumberCompact(data[GetClusterResponseStatus.NOT_READY])}
					</Text>
					<Anchor asChild>
						<Link
							search={{
								filters: [
									{
										id: "status",
										value: [
											getStatusLabel(GetClusterResponseStatus.NOT_READY),
											getStatusLabel(GetClusterResponseStatus.CORDON),
											getStatusLabel(GetClusterResponseStatus.CORDON_AND_DRAIN),
										],
									},
								],
							}}
							to="/clusters"
							viewTransition
						>
							Not Ready
						</Link>
					</Anchor>
				</div>
			</div>
		</Panel>
	);
}

export function ClusterStatsPanel({ ncaId }: { ncaId: string }) {
	return (
		<StatPanelBoundary
			skeletonCount={3}
			slotHeading="Compute Clusters"
			slotIcon={<BoxesIcon />}
		>
			<ClusterStatsPanelContent ncaId={ncaId} />
		</StatPanelBoundary>
	);
}
