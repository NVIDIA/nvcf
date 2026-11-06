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
	Tag,
	Text,
} from "@nvidia/foundations-react-core";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Lightbulb } from "lucide-react";
import { CodeSnippet } from "~/components/CodeSnippet";
import { CopyText } from "~/components/CopyText";
import { DetailList } from "~/components/DetailList";
import { OverflowGroup } from "~/components/OverflowGroup";
import { getActiveNcaId } from "~/features/accounts/getActiveNcaId";
import { InstanceTypesPanel } from "~/features/functions/components/InstanceTypesPanel";
import { StatusBadge } from "~/features/functions/components/StatusBadge";
import {
	DEPLOYED_FUNCTION_STATUSES,
	FUNCTION_TYPE,
} from "~/features/functions/constants";
import { getGetFunctionDeploymentQueryOptions } from "~/generated/api/function-deployment/function-deployment";
import {
	getGetFunctionVersionQueryOptions,
	useGetFunctionVersionSuspense,
} from "~/generated/api/function-management/function-management";
import { DETAIL_REFETCH_INTERVAL } from "~/lib/queryClient";
import { formatDateTime } from "~/utils/formatters";

export const Route = createFileRoute(
	"/functions/$functionId/versions/$versionId",
)({
	staticData: { account: { redirectOnChange: "/functions" } },
	loader: async ({ context: { queryClient }, params }) => {
		const ncaId = await getActiveNcaId(queryClient);
		const data = await queryClient.ensureQueryData({
			...getGetFunctionVersionQueryOptions(
				ncaId,
				params.functionId,
				params.versionId,
			),
			revalidateIfStale: true,
		});
		const fn = data?.function;
		if (fn && DEPLOYED_FUNCTION_STATUSES.includes(fn.status)) {
			void queryClient.prefetchQuery(
				getGetFunctionDeploymentQueryOptions(
					ncaId,
					params.functionId,
					params.versionId,
				),
			);
		}
		return { ncaId, functionName: fn?.name };
	},
	head: ({ loaderData }) => ({
		meta: [{ title: `${loaderData?.functionName ?? "Function"} · NVCF` }],
	}),
	component: FunctionDetail,
});

function FunctionDetail() {
	const { ncaId } = Route.useLoaderData();
	const { functionId, versionId } = Route.useParams();

	const { data: fnData } = useGetFunctionVersionSuspense(
		ncaId,
		functionId,
		versionId,
		undefined,
		{ query: { refetchInterval: DETAIL_REFETCH_INTERVAL } },
	);
	const fn = fnData.function;

	const invocationCommand = [
		"curl --request POST \\",
		`  --url "https://\${GATEWAY_ADDR}/echo" \\`,
		`  --header "Host: ${fn.id}.invocation.\${GATEWAY_ADDR}" \\`,
		`  --header "Authorization: Bearer \${API_KEY}" \\`,
		'  --header "Content-Type: application/json" \\',
		'  --data \'{"message": "hello"}\'',
	].join("\n");

	const envContent = fn.containerEnvironment?.length
		? fn.containerEnvironment.map((e) => `${e.key}=${e.value}`).join("\n")
		: undefined;

	const rateLimit = fn.rateLimit;
	const globalRateLimits = rateLimit?.rateLimit
		?.split(",")
		.map((r) => r.trim());
	const reconfiguredNcaIds =
		rateLimit?.perNcaIdRate && Object.keys(rateLimit.perNcaIdRate).length
			? Object.entries(rateLimit.perNcaIdRate)
					.map(([k, v]) => `${k}: ${v}`)
					.join(", ")
			: undefined;

	return (
		<div className="flex flex-col gap-6">
			<PageHeader
				kind="flat"
				slotBreadcrumbs={
					<Breadcrumbs
						items={[
							{
								children: (
									<Link to="/functions" viewTransition>
										Functions
									</Link>
								),
							},
							fn.name,
						]}
					/>
				}
				slotHeading={fn.name}
			/>

			<div className="grid grid-cols-1 gap-6 lg:grid-cols-2 lg:gap-x-8">
				<Panel elevation="high" slotHeading="Basic Details">
					<DetailList
						items={[
							{ label: "Status", value: <StatusBadge status={fn.status} /> },
							{
								label: "Function ID",
								value: <CopyText kind="body/regular/md">{fn.id}</CopyText>,
							},
							{
								label: "Version ID",
								value: (
									<CopyText kind="body/regular/md">{fn.versionId}</CopyText>
								),
							},
							{
								label: "Created Date",
								value: formatDateTime(fn.createdAt),
							},
							{
								label: "Function Type",
								value: fn.helmChart
									? FUNCTION_TYPE.HELM
									: FUNCTION_TYPE.CONTAINER,
							},
							{ label: "Description", value: fn.description },
							{
								label: "Tags",
								value: fn.tags?.length ? (
									<OverflowGroup className="w-full" kind="popover">
										{fn.tags.map((tag) => (
											<Tag color="teal" key={tag} kind="solid" readOnly>
												{tag}
											</Tag>
										))}
									</OverflowGroup>
								) : undefined,
							},
						]}
					/>
				</Panel>

				<Panel elevation="high" slotHeading="Invocation">
					<CodeSnippet
						language="shell"
						slotActions={
							<div className="w-full">
								<Text className="text-secondary" kind="label/semibold/sm">
									Invoke Command
								</Text>
							</div>
						}
						value={invocationCommand}
					/>
				</Panel>

				<div className="flex flex-col gap-6">
					{fn.helmChart ? (
						<Panel elevation="high" slotHeading="Helm Chart Details">
							<DetailList
								items={[
									{ label: "Helm Chart", value: fn.helmChart },
									{
										label: "Models",
										value: fn.models?.map((m) => m.uri ?? m.name),
									},
									{
										label: "Model Mount Points",
										value: fn.models?.map((m) => `/config/models/${m.name}`),
									},
									{
										label: "Resources",
										value: fn.resources?.map((r) => r.uri),
									},
									{
										label: "Resource Mount Points",
										value: fn.resources?.map(
											(m) => `/config/resources/${m.name}`,
										),
									},
									{ label: "Secrets", value: fn.secrets },
									{
										label: "Helm Chart Overrides",
										value: fn.containerArgs ? (
											<CodeSnippet value={fn.containerArgs} />
										) : undefined,
									},
								]}
							/>
						</Panel>
					) : (
						<Panel elevation="high" slotHeading="Container Details">
							<DetailList
								items={[
									{ label: "Container Image", value: fn.containerImage },
									{
										label: "Models",
										value: fn.models?.map((m) => m.uri ?? m.name),
									},
									{
										label: "Model Mount Points",
										value: fn.models?.map((m) => `/config/models/${m.name}`),
									},
									{
										label: "Resources",
										value: fn.resources?.map((r) => r.uri),
									},
									{
										label: "Resource Mount Points",
										value: fn.resources?.map(
											(m) => `/config/resources/${m.name}`,
										),
									},
									{ label: "Secrets", value: fn.secrets },
									{
										label: "Environment",
										value: envContent ? (
											<CodeSnippet value={envContent} />
										) : undefined,
									},
									{
										label: "Container Args",
										value: fn.containerArgs ? (
											<CodeSnippet value={fn.containerArgs} />
										) : undefined,
									},
								]}
							/>
						</Panel>
					)}

					<Panel elevation="high" slotHeading="Endpoint and Health">
						<DetailList
							items={[
								{
									label: "Low Latency Streaming",
									value: fn.functionType === "STREAMING" ? "On" : "Off",
								},
								{ label: "Health Protocol", value: fn.health?.protocol },
								{ label: "Health Port", value: fn.health?.port },
								{ label: "Health Endpoint", value: fn.health?.uri },
								{ label: "Inference Port", value: fn.inferencePort },
								{ label: "Inference Endpoint", value: fn.inferenceUrl },
							]}
						/>
					</Panel>
				</div>

				<div className="flex flex-col gap-6">
					{DEPLOYED_FUNCTION_STATUSES.includes(fn.status) ? (
						<InstanceTypesPanel
							functionId={functionId}
							ncaId={ncaId}
							versionId={versionId}
						/>
					) : (
						<Panel elevation="high" slotHeading="Instance Types">
							<StatusMessage
								slotHeading="No Instance Type Yet"
								slotMedia={<Lightbulb size={32} />}
								slotSubheading="Instance types will appear once function is deployed"
							/>
						</Panel>
					)}

					<Panel elevation="high" slotHeading="Rate Limit">
						<DetailList
							items={[
								{ label: "Global Rate Limits", value: globalRateLimits },
								{
									label: "Override Rate Limits: Excluded NCA IDs",
									value: rateLimit?.exemptedNcaIds,
								},
								{
									label: "Override Rate Limits: Reconfigured NCA IDs",
									value: reconfiguredNcaIds,
								},
							]}
						/>
					</Panel>
				</div>
			</div>
		</div>
	);
}
