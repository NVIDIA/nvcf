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

import { Anchor, Skeleton, Tag, Text } from "@nvidia/foundations-react-core";
import { Link } from "@tanstack/react-router";
import clsx from "clsx";
import { uniq } from "lodash-es";
import type { ReactNode } from "react";
import { CopyText } from "~/components/CopyText";
import { OverflowGroup } from "~/components/OverflowGroup";
import { formatDateTime } from "~/utils/formatters";
import type { FunctionWithDeployment } from "../types";
import { StatusBadge } from "./StatusBadge";

function Field({
	label,
	children,
	className,
}: {
	label: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<div className={clsx("flex min-w-0 flex-col gap-1.5", className)}>
			<Text className="text-secondary" kind="label/bold/sm">
				{label}
			</Text>
			{children}
		</div>
	);
}

function FieldSkeleton({
	labelWidth,
	children,
	className,
}: {
	labelWidth: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<div className={clsx("flex min-w-0 flex-col gap-1.5", className)}>
			<Skeleton className={`h-3 ${labelWidth}`} kind="line" />
			{children}
		</div>
	);
}

export function FunctionCardSkeleton() {
	return (
		<div className="flex flex-col gap-4 border-b border-base p-4">
			<div className="flex flex-col gap-2">
				<Skeleton className="h-5 w-48" kind="line" />
				<Skeleton className="h-3 w-32" kind="line" />
			</div>

			<div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
				<div className="flex min-w-0 gap-6">
					<FieldSkeleton className="min-w-25" labelWidth="w-12">
						<Skeleton className="h-6 w-20" kind="pill" />
					</FieldSkeleton>
					<FieldSkeleton className="flex-1" labelWidth="w-24">
						<Skeleton className="h-4 w-32" kind="line" />
					</FieldSkeleton>
				</div>

				<FieldSkeleton labelWidth="w-24">
					<Skeleton className="h-4 w-full" kind="line" />
				</FieldSkeleton>

				<FieldSkeleton labelWidth="w-28">
					<div className="flex gap-2">
						<Skeleton className="h-6 w-20" kind="pill" />
						<Skeleton className="h-6 w-16" kind="pill" />
					</div>
				</FieldSkeleton>

				<FieldSkeleton labelWidth="w-10">
					<Skeleton className="h-6 w-16" kind="pill" />
				</FieldSkeleton>
			</div>
		</div>
	);
}

export function FunctionCard({ fn }: { fn: FunctionWithDeployment }) {
	const instanceTypes = uniq(
		fn.deploymentSpecifications.map((s) => s.instanceType).filter(Boolean),
	);

	return (
		<div className="flex flex-col gap-4 border-b border-base p-4">
			<div className="flex flex-col gap-2">
				<Anchor asChild>
					<Link
						params={{ functionId: fn.id, versionId: fn.versionId }}
						to="/functions/$functionId/versions/$versionId"
						viewTransition
					>
						<Text kind="label/bold/md">{fn.name}</Text>
					</Link>
				</Anchor>
				<Text className="text-secondary" kind="label/regular/xs">
					{fn.helmChart ? "Helm Function" : "Container Function"}
				</Text>
			</div>

			<div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
				<div className="flex min-w-0 gap-6">
					<Field className="min-w-25" label="Status">
						<StatusBadge status={fn.status} />
					</Field>
					<Field className="flex-1" label="Created Date">
						<Text kind="label/semibold/sm">{formatDateTime(fn.createdAt)}</Text>
					</Field>
				</div>

				<Field label="Function ID">
					<CopyText kind="label/semibold/sm">{fn.id}</CopyText>
				</Field>

				<Field label="Instance Types">
					{instanceTypes.length > 0 ? (
						<OverflowGroup kind="popover">
							{instanceTypes.map((type) => (
								<Tag color="gray" key={type} kind="solid" readOnly>
									{type}
								</Tag>
							))}
						</OverflowGroup>
					) : (
						<Text kind="label/semibold/sm">&mdash;</Text>
					)}
				</Field>

				<Field label="Tags">
					{fn.tags && fn.tags.length > 0 ? (
						<OverflowGroup kind="popover">
							{fn.tags.map((tag) => (
								<Tag color="teal" key={tag} kind="solid" readOnly>
									{tag}
								</Tag>
							))}
						</OverflowGroup>
					) : (
						<Text kind="label/semibold/sm">&mdash;</Text>
					)}
				</Field>
			</div>
		</div>
	);
}
