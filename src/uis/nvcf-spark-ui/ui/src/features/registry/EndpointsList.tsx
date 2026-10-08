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

import { Banner, Panel, StatusMessage } from "@nvidia/foundations-react-core";
import { createLazyRoute } from "@tanstack/react-router";
import { SearchXIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { useGetConfig } from "~/generated/api/config/config";
import { useGetRegistrySuspense } from "~/generated/api/registry/registry";
import { formatClock } from "~/utils/time";
import { EndpointDetail } from "./components/EndpointDetail";
import { EndpointsTable } from "./components/EndpointsTable";
import { RegistryEmpty } from "./components/RegistryEmpty";
import { RegistryPageHeading } from "./components/RegistryPageHeading";
import { useHealthChanges } from "./hooks/useHealthChanges";
import { isUnreachable } from "./utils";

export const EndpointsListRoute = createLazyRoute("/registry")({
	component: EndpointsList,
});

function EndpointsList() {
	const { model: selectedParam } = EndpointsListRoute.useSearch();
	const navigate = EndpointsListRoute.useNavigate();
	// The shell polls the registry; this view re-renders in place as polls land.
	const registry = useGetRegistrySuspense();
	const { data: config } = useGetConfig();
	const { models } = registry.data;
	const healthChanges = useHealthChanges(models);
	// Without a selection, show the first endpoint so the panel is never blank.
	const selected = selectedParam ?? models[0]?.model;
	const endpoint = models.find((m) => m.model === selected);

	// On narrow screens the panel sits below the table; bring it into view when
	// the user picks a row, but not on page load or a poll.
	const detailRef = useRef<HTMLDivElement>(null);
	const revealDetail = useRef(false);
	useEffect(() => {
		if (!revealDetail.current || selected === undefined) return;
		revealDetail.current = false;
		detailRef.current?.scrollIntoView?.({ block: "nearest" });
	}, [selected]);

	return (
		<div className="flex flex-col gap-6">
			<RegistryPageHeading />
			{registry.isError && (
				<RefreshFailed
					error={registry.error}
					updatedAt={registry.dataUpdatedAt}
				/>
			)}
			{models.length === 0 ? (
				<RegistryEmpty />
			) : (
				<div className="grid grid-cols-[minmax(0,1fr)] items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,28rem)] xl:grid-cols-[minmax(0,1fr)_minmax(0,34rem)]">
					<EndpointsTable
						models={models}
						onReveal={() => {
							revealDetail.current = true;
						}}
						onSelect={(model) => {
							revealDetail.current = true;
							void navigate({
								search: { model },
								replace: true,
								resetScroll: false,
							});
						}}
						selected={selected}
					/>
					<div
						className="scroll-mt-[calc(var(--nv-app-bar-height)+1rem)] lg:sticky lg:top-[calc(var(--nv-app-bar-height)+1.5rem)]"
						ref={detailRef}
					>
						{endpoint ? (
							<EndpointDetail
								config={config}
								endpoint={endpoint}
								healthSince={healthChanges.get(endpoint.model)}
							/>
						) : (
							<Panel>
								<StatusMessage
									size="small"
									slotHeading={
										<h2 className="[overflow-wrap:anywhere]">
											{selected} isn't registered
										</h2>
									}
									slotMedia={<SearchXIcon />}
									slotSubheading="It was removed, or the gateway restarted and it hasn't registered again yet. This page updates as soon as it does."
								/>
							</Panel>
						)}
					</div>
				</div>
			)}
		</div>
	);
}

/**
 * A poll failed after the registry loaded: keep showing the last data, and say
 * as of when, instead of replacing the page with an error.
 */
function RefreshFailed({
	error,
	updatedAt,
}: {
	error: unknown;
	updatedAt: number;
}) {
	const asOf = formatClock(updatedAt);
	return (
		<Banner status="error">
			{isUnreachable(error)
				? `Gateway unreachable. Showing the registry as of ${asOf}; it updates once the gateway responds.`
				: `Couldn't refresh the registry. Showing the registry as of ${asOf}.`}
		</Banner>
	);
}
