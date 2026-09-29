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

import { Button, Divider, Panel, Text } from "@nvidia/foundations-react-core";
import { createLazyRoute, Link } from "@tanstack/react-router";
import { BoxesIcon, ChevronRight, FunctionSquareIcon } from "lucide-react";
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { getStatusLabel } from "../clusters/components/StatusBadge";
import { ClusterStatsPanel } from "./components/ClusterStatsPanel";
import { ControlPlaneStatsPanel } from "./components/ControlPlaneStatsPanel";
import { FunctionStatsPanel } from "./components/FunctionStatsPanel";
import { PageHeading } from "./components/PageHeading";
import { ReadyClusterCards } from "./components/ReadyClustersPanel";
import { RecentFunctionsList } from "./components/RecentFunctionsList";

export const Route = createLazyRoute("/")({
	component: Dashboard,
});

function Dashboard() {
	const { ncaId } = Route.useLoaderData();

	return (
		<div className="flex flex-col gap-6">
			<PageHeading />
			<div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">
				<ControlPlaneStatsPanel />
				<ClusterStatsPanel ncaId={ncaId} />
				<FunctionStatsPanel ncaId={ncaId} />
			</div>
			<Divider />
			<section
				aria-labelledby="ready-clusters-heading"
				className="flex flex-col gap-6"
			>
				<div className="flex justify-between items-center">
					<div className="flex gap-2 items-center">
						<BoxesIcon size="1em" />
						<Text id="ready-clusters-heading" kind="title/xs">
							Ready Compute Clusters
						</Text>
					</div>
					<Button asChild kind="tertiary" size="small">
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
							View All <ChevronRight size="1em" />
						</Link>
					</Button>
				</div>
				<ReadyClusterCards ncaId={ncaId} />
			</section>
			<Divider />
			<Panel
				attributes={{
					PanelHeader: {
						className: "-mx-6 px-6 pb-4 border-b border-base",
					},
				}}
				elevation="high"
				slotHeading={
					<div className="flex items-center justify-between">
						Recent Functions
						<Button asChild kind="tertiary" size="small">
							<Link to="/functions" viewTransition>
								View All <ChevronRight size="1em" />
							</Link>
						</Button>
					</div>
				}
				slotIcon={<FunctionSquareIcon />}
			>
				<RecentFunctionsList ncaId={ncaId} />
			</Panel>
		</div>
	);
}
