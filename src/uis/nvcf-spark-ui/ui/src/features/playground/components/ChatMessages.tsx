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
	AnimatedChevron,
	Banner,
	Button,
	Collapsible,
	Skeleton,
	Text,
} from "@nvidia/foundations-react-core";
import { memo } from "react";
import { formatDuration } from "~/utils/time";
import type { AssistantTurn, UserTurn } from "../hooks/useChat";
import { Markdown } from "./Markdown";

export const UserMessage = memo(function UserMessage({
	turn,
}: {
	turn: UserTurn;
}) {
	return (
		<div className="ml-auto max-w-[min(700px,85%)] rounded-tl-xl rounded-tr-xl rounded-bl-xl bg-background-subtle p-4">
			<Text
				className="whitespace-pre-wrap [overflow-wrap:anywhere]"
				kind="body/regular/md"
			>
				{turn.content}
			</Text>
		</div>
	);
});

interface AssistantMessageProps {
	turn: AssistantTurn;
	/** Offered on a failed reply when it is the latest one. */
	onRetry?: () => void;
}

/**
 * One reply: the model's reasoning (collapsed), the answer as it streams, and
 * timing and usage once it ends.
 */
export const AssistantMessage = memo(function AssistantMessage({
	turn,
	onRetry,
}: AssistantMessageProps) {
	const waiting =
		turn.status === "streaming" && !turn.reasoning && !turn.content;
	return (
		<div className="flex flex-col gap-2 sm:pr-10">
			{turn.reasoning && <Reasoning turn={turn} />}
			{waiting && (
				<div className="flex flex-col gap-2">
					<Text className="text-placeholder" kind="body/regular/md">
						Waiting for the model…
					</Text>
					<Skeleton className="h-2 w-full" kind="pill" />
					<Skeleton className="h-2 w-1/2" kind="pill" />
				</div>
			)}
			{turn.content && <Markdown>{turn.content}</Markdown>}
			{turn.status === "error" && turn.error ? (
				<Banner
					slotActions={
						onRetry && (
							<Button kind="secondary" onClick={onRetry} size="tiny">
								Try again
							</Button>
						)
					}
					status="error"
				>
					{turn.error.title}. {turn.error.detail}
				</Banner>
			) : (
				<ReplyStats turn={turn} />
			)}
		</div>
	);
});

function Reasoning({ turn }: { turn: AssistantTurn }) {
	const thinking = turn.status === "streaming" && !turn.content;
	const end = turn.answerStartMs ?? turn.totalMs;
	const label =
		thinking || turn.firstTokenMs === undefined || end === undefined
			? "Thinking…"
			: `Thought for ${formatDuration(end - turn.firstTokenMs)}`;
	return (
		<Collapsible
			slotTrigger={
				<Button asChild kind="tertiary" size="small">
					<div>
						{label}
						<AnimatedChevron />
					</div>
				</Button>
			}
		>
			<Markdown className="border-base border-l-2 pl-3 text-secondary">
				{turn.reasoning}
			</Markdown>
		</Collapsible>
	);
}

/** "412 tokens · first token 680 ms · 3.2 s", plus "Stopped" when cut short. */
function ReplyStats({ turn }: { turn: AssistantTurn }) {
	if (turn.status === "streaming") return null;
	const parts = [
		turn.status === "stopped" ? "Stopped" : undefined,
		turn.usage ? `${turn.usage.completion_tokens} tokens` : undefined,
		turn.firstTokenMs !== undefined
			? `first token ${formatDuration(turn.firstTokenMs)}`
			: undefined,
		turn.totalMs !== undefined ? formatDuration(turn.totalMs) : undefined,
	].filter(Boolean);
	return (
		<Text className="text-secondary" kind="label/regular/sm">
			{parts.join(" · ")}
		</Text>
	);
}
