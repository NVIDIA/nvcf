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

import { Anchor, Modal, Panel, Text } from "@nvidia/foundations-react-core";
import clsx from "clsx";
import { AlertTriangleIcon, ShipWheelIcon } from "lucide-react";
import { useState } from "react";
import { useGetControlPlaneStatusSuspense } from "~/generated/api/control-plane/control-plane";
import type { ControlPlaneComponentStatus } from "~/generated/model/controlPlaneComponentStatus";
import { formatDateTime, formatNumberCompact } from "~/utils/formatters";
import { StatPanelBoundary } from "./StatPanelBoundary";

const controlPlaneSelect = (data: ControlPlaneComponentStatus[]) => ({
	healthy: data.filter((c) => c.status === "healthy").length,
	unhealthy: data.filter((c) => c.status === "unhealthy").length,
	unhealthyComponents: data.filter((c) => c.status === "unhealthy"),
});

function ControlPlaneStatsPanelContent() {
	const { data } = useGetControlPlaneStatusSuspense({
		query: { select: controlPlaneSelect },
	});
	const [modalOpen, setModalOpen] = useState(false);

	return (
		<Panel
			elevation="high"
			slotHeading="Control Plane Components"
			slotIcon={<ShipWheelIcon />}
		>
			<div className="flex flex-wrap items-baseline gap-x-6 gap-y-2">
				<div className="flex items-baseline gap-2">
					<Text kind="display/md">{formatNumberCompact(data.healthy)}</Text>
					<Text kind="body/regular/md">Healthy</Text>
				</div>
				<div className="flex items-baseline gap-2">
					<Text
						className={clsx(data.unhealthy > 0 && "text-accent-red")}
						kind="display/md"
					>
						{formatNumberCompact(data.unhealthy)}
					</Text>
					{data.unhealthy > 0 ? (
						<Anchor asChild>
							<button onClick={() => setModalOpen(true)} type="button">
								Unhealthy
							</button>
						</Anchor>
					) : (
						<Text kind="body/regular/md">Unhealthy</Text>
					)}
				</div>
			</div>
			{data.unhealthy > 0 && (
				<Modal
					className="w-11/12 max-w-125"
					onOpenChange={setModalOpen}
					open={modalOpen}
					slotHeading={
						<span className="flex items-center gap-2">
							<AlertTriangleIcon className="text-accent-red" size="1em" />
							{data.unhealthy}{" "}
							{data.unhealthy === 1 ? "Component" : "Components"} In Error
						</span>
					}
				>
					<div className="flex flex-col gap-1.5">
						{data.unhealthyComponents.map((component) => (
							<div
								className="bg-surface-raised rounded-md grid grid-cols-[200px_1fr] gap-3 items-center p-3 pt-2.5"
								key={component.componentName}
							>
								<div className="flex flex-col gap-0.5">
									<Text kind="body/semibold/md">{component.componentName}</Text>
									<Text className="text-subtle" kind="label/regular/xs">
										{component.namespace}
									</Text>
								</div>
								<Text className="text-secondary" kind="label/regular/sm">
									Since {formatDateTime(component.timestamp)}
								</Text>
							</div>
						))}
					</div>
				</Modal>
			)}
		</Panel>
	);
}

export function ControlPlaneStatsPanel() {
	return (
		<StatPanelBoundary
			skeletonCount={2}
			slotHeading="Control Plane Components"
			slotIcon={<ShipWheelIcon />}
		>
			<ControlPlaneStatsPanelContent />
		</StatPanelBoundary>
	);
}
