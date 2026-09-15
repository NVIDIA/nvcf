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

import { Button, Panel, StatusMessage } from "@nvidia/foundations-react-core";
import { AlertTriangleIcon } from "lucide-react";
import type { ReactNode } from "react";
import { AsyncBoundary } from "~/components/AsyncBoundary";
import { StatPanelSkeleton } from "./StatPanelSkeleton";

interface StatPanelBoundaryProps {
	slotHeading: string;
	slotIcon: ReactNode;
	skeletonCount: number;
	children: ReactNode;
}

export function StatPanelBoundary({
	slotHeading,
	slotIcon,
	skeletonCount,
	children,
}: StatPanelBoundaryProps) {
	return (
		<AsyncBoundary
			errorFallback={({ resetErrorBoundary }) => (
				<Panel elevation="high" slotHeading={slotHeading} slotIcon={slotIcon}>
					<StatusMessage
						size="small"
						slotFooter={
							<Button kind="secondary" onClick={resetErrorBoundary}>
								Retry
							</Button>
						}
						slotHeading="Failed to load data."
						slotMedia={<AlertTriangleIcon />}
					/>
				</Panel>
			)}
			fallback={
				<StatPanelSkeleton
					count={skeletonCount}
					slotHeading={slotHeading}
					slotIcon={slotIcon}
				/>
			}
		>
			{children}
		</AsyncBoundary>
	);
}
