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
	Skeleton,
	StatusMessage,
	Tag,
	Text,
} from "@nvidia/foundations-react-core";
import { QueryErrorResetBoundary } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import dayjs from "dayjs";
import { uniq } from "lodash-es";
import { CircleAlertIcon } from "lucide-react";
import { Suspense } from "react";
import { ErrorBoundary } from "react-error-boundary";
import { useFunctionsWithDeployments } from "~/features/functions/hooks/useFunctionsWithDeployments";
import type { FunctionWithDeployment } from "~/features/functions/types";
import { formatDateTime } from "~/utils/formatters";
import { StatusBadge } from "../../functions/components/StatusBadge";

const RECENT_COUNT = 5;

function selectRecent({
	functions,
}: {
	functions: FunctionWithDeployment[];
}): FunctionWithDeployment[] {
	return functions
		.filter((fn) => Boolean(fn.lastUpdatedAt))
		.toSorted((a, b) => dayjs(b.lastUpdatedAt).diff(dayjs(a.lastUpdatedAt)))
		.slice(0, RECENT_COUNT);
}

function RecentFunctionRow({ fn }: { fn: FunctionWithDeployment }) {
	const instanceTypes = uniq(
		fn.deploymentSpecifications.map((s) => s.instanceType).filter(Boolean),
	);

	return (
		<div className="flex items-start -mx-6 border-b border-base px-6 pb-6">
			<div className="flex min-w-0 flex-1 flex-col gap-2">
				<div className="flex items-center justify-between">
					<Anchor asChild textKind="body/regular/lg">
						<Link
							params={{ functionId: fn.id, versionId: fn.versionId }}
							to="/functions/$functionId/versions/$versionId"
							viewTransition
						>
							{fn.name}
						</Link>
					</Anchor>
					<StatusBadge status={fn.status} />
				</div>

				<div className="flex flex-col gap-4">
					<Text className="text-secondary" kind="label/semibold/sm">
						{fn.id}
						{fn.lastUpdatedAt && (
							<>
								<span className="px-2">·</span>Last updated{" "}
								{formatDateTime(fn.lastUpdatedAt)}
							</>
						)}
					</Text>

					{(instanceTypes.length > 0 || (fn.tags && fn.tags.length > 0)) && (
						<div className="flex flex-wrap gap-2">
							{instanceTypes.map((t) => (
								<Tag color="gray" key={t} kind="solid" readOnly>
									{t}
								</Tag>
							))}
							{fn.tags?.map((t) => (
								<Tag color="teal" key={t} kind="solid" readOnly>
									{t}
								</Tag>
							))}
						</div>
					)}
				</div>
			</div>
		</div>
	);
}

function RecentFunctionsSkeleton() {
	return (
		<div className="grid grid-rows-5 gap-6">
			{Array.from({ length: RECENT_COUNT }, (_, i) => (
				<div
					className="flex items-start -mx-6 border-b border-base px-6 pb-6"
					// biome-ignore lint/suspicious/noArrayIndexKey: <skeleton loader>
					key={i}
				>
					<div className="flex min-w-0 flex-1 flex-col gap-4">
						<div className="flex items-center justify-between gap-2">
							<Skeleton className="h-5 w-48" kind="line" />
							<Skeleton className="h-5 w-16" kind="pill" />
						</div>
						<Skeleton className="h-3 w-72" kind="line" />
						<div className="flex gap-2">
							<Skeleton className="h-6 w-20" kind="pill" />
							<Skeleton className="h-6 w-16" kind="pill" />
						</div>
					</div>
				</div>
			))}
		</div>
	);
}

function RecentFunctionsListContent({ ncaId }: { ncaId: string }) {
	const recentFunctions = useFunctionsWithDeployments(ncaId, selectRecent);

	if (recentFunctions.length === 0) {
		return (
			<StatusMessage
				className="min-h-48"
				slotHeading="No deployed functions yet"
				slotMedia={<CircleAlertIcon size={36} />}
			/>
		);
	}

	return (
		<div className="grid grid-rows-5 gap-6">
			{recentFunctions.map((fn) => (
				<RecentFunctionRow fn={fn} key={fn.versionId} />
			))}
		</div>
	);
}

export function RecentFunctionsList({ ncaId }: { ncaId: string }) {
	return (
		<QueryErrorResetBoundary>
			{({ reset }) => (
				<ErrorBoundary
					fallbackRender={({ resetErrorBoundary }) => (
						<StatusMessage
							slotFooter={
								<Button kind="secondary" onClick={resetErrorBoundary}>
									Retry
								</Button>
							}
							slotHeading="Failed to load functions."
							slotMedia={<CircleAlertIcon />}
						/>
					)}
					onReset={reset}
				>
					<Suspense fallback={<RecentFunctionsSkeleton />}>
						<RecentFunctionsListContent ncaId={ncaId} />
					</Suspense>
				</ErrorBoundary>
			)}
		</QueryErrorResetBoundary>
	);
}
