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
	Banner,
	Button,
	PageHeader,
	StatusMessage,
	Text,
} from "@nvidia/foundations-react-core";
import { createLazyRoute, Link } from "@tanstack/react-router";
import { MessagesSquareIcon, RotateCcwIcon } from "lucide-react";
import { HealthStatus } from "~/features/registry/components/HealthStatus";
import { useGetRegistrySuspense } from "~/generated/api/registry/registry";
import { RegistryHealth } from "~/generated/model/registryHealth";
import { ChatComposer } from "./components/ChatComposer";
import { AssistantMessage, UserMessage } from "./components/ChatMessages";
import { type ChatTurn, useChat } from "./hooks/useChat";
import { useStickToBottom } from "./hooks/useStickToBottom";
import { EXAMPLE_PROMPT } from "./utils";

export const PlaygroundRoute = createLazyRoute("/playground")({
	component: Playground,
});

function Playground() {
	const { model } = PlaygroundRoute.useSearch();
	// Keyed so that a different model starts a fresh conversation.
	return model ? <ChatWorkspace key={model} model={model} /> : <NoModel />;
}

function NoModel() {
	return (
		<div className="grid min-h-[50dvh] place-items-center">
			<StatusMessage
				slotFooter={
					<Button asChild color="brand">
						<Link to="/registry">Go to endpoint registry</Link>
					</Button>
				}
				slotHeading={<h1>No model selected</h1>}
				slotMedia={<MessagesSquareIcon />}
				slotSubheading="Open the playground from an endpoint in the registry."
			/>
		</div>
	);
}

/** Focused chat with one model (KUI workspace page, focused-chat variant). */
function ChatWorkspace({ model }: { model: string }) {
	// The shell keeps polling the registry, so health here stays current.
	const { data } = useGetRegistrySuspense();
	const endpoint = data.models.find((m) => m.model === model);
	const routable = endpoint?.health === RegistryHealth.Healthy;
	const chat = useChat(model);
	useStickToBottom(chat.turns);

	return (
		// Fills the viewport below the AppBar and main's padding, so the composer
		// rests at the bottom even before the first message.
		<div className="mx-auto flex min-h-[calc(100dvh-var(--nv-app-bar-height)-3rem)] w-full max-w-[800px] flex-col gap-6">
			<PageHeader
				kind="flat"
				slotActions={
					chat.turns.length > 0 && (
						<Button
							className="pointer-coarse:min-h-11"
							kind="tertiary"
							onClick={chat.reset}
							size="small"
						>
							<RotateCcwIcon aria-hidden />
							New chat
						</Button>
					)
				}
				slotHeading={
					<span className="flex flex-wrap items-center gap-x-3 gap-y-1">
						<h1 className="[overflow-wrap:anywhere]">{model}</h1>
						{endpoint && <HealthStatus health={endpoint.health} />}
					</span>
				}
				slotSubheading="Playground"
			/>

			{!routable && (
				<Banner
					slotActions={
						<Button asChild kind="secondary" size="tiny">
							<Link search={{ model }} to="/registry">
								View in registry
							</Link>
						</Button>
					}
					status="warning"
				>
					{endpoint
						? "This endpoint is unhealthy, so the gateway can't route to it. Chat comes back as soon as it's healthy."
						: "This model isn't registered with the gateway. Chat comes back as soon as it registers."}
				</Banner>
			)}

			<section aria-label="Conversation" className="flex flex-1 flex-col gap-6">
				{chat.turns.length === 0 ? (
					<div className="flex flex-1 flex-col items-center justify-center gap-2 py-12 text-center">
						<Text asChild kind="title/sm">
							<h2>Try {model}</h2>
						</Text>
						<Text className="max-w-md text-secondary" kind="body/regular/md">
							Messages go through the LLM API Gateway, and the reply streams in
							as the model writes it.
						</Text>
					</div>
				) : (
					chat.turns.map((turn, index) =>
						turn.role === "user" ? (
							<UserMessage key={turn.id} turn={turn} />
						) : (
							<AssistantMessage
								key={turn.id}
								onRetry={
									index === chat.turns.length - 1 ? chat.retry : undefined
								}
								turn={turn}
							/>
						),
					)
				)}
			</section>

			<p aria-live="polite" className="sr-only">
				{announcement(chat.turns.at(-1))}
			</p>

			<div className="sticky bottom-0 bg-surface-sunken pt-2 pb-[max(1rem,env(safe-area-inset-bottom))]">
				<ChatComposer
					disabled={!routable}
					initialValue={EXAMPLE_PROMPT}
					onSend={chat.send}
					onStop={chat.stop}
					streaming={chat.streaming}
				/>
			</div>
		</div>
	);
}

/**
 * What a screen reader hears about the latest reply. Streamed tokens aren't
 * announced one by one; the outcome is.
 */
function announcement(turn: ChatTurn | undefined): string {
	if (turn?.role !== "assistant") return "";
	switch (turn.status) {
		case "streaming":
			return "The model is replying";
		case "done":
			return "Reply complete";
		case "stopped":
			return "Reply stopped";
		case "error":
			return turn.error?.title ?? "Reply failed";
	}
}
