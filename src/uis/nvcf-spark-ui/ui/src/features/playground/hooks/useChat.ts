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

import { useCallback, useEffect, useRef, useState } from "react";
import type { ChatMessage } from "~/generated/model/chatMessage";
import type { CompletionUsage } from "~/generated/model/completionUsage";
import { streamChatCompletion } from "~/lib/sse";
import { type ChatErrorInfo, describeChatError } from "../utils";

export interface UserTurn {
	id: string;
	role: "user";
	content: string;
}

export interface AssistantTurn {
	id: string;
	role: "assistant";
	content: string;
	/** `delta.reasoning_content`, which reasoning models such as GLM stream before the answer. */
	reasoning: string;
	status: "streaming" | "done" | "stopped" | "error";
	error?: ChatErrorInfo;
	/** From the final chunk (`stream_options.include_usage`). */
	usage?: CompletionUsage;
	/** Milliseconds from sending to the first token, reasoning or answer. */
	firstTokenMs?: number;
	/** Milliseconds from sending to the first answer token, which ends the reasoning. */
	answerStartMs?: number;
	/** Milliseconds from sending to the end of the stream. */
	totalMs?: number;
}

export type ChatTurn = UserTurn | AssistantTurn;

// crypto.randomUUID needs a secure context, and the demo may be served over
// plain HTTP, so ids come from a counter.
let lastId = 0;
const nextId = () => `turn-${++lastId}`;

/**
 * The conversation so far as chat messages. Turns come in user/assistant
 * pairs; a pair whose reply failed or is empty is left out, so the roles keep
 * alternating, which some chat templates require.
 */
export function toMessages(turns: ChatTurn[]): ChatMessage[] {
	const messages: ChatMessage[] = [];
	for (let i = 0; i + 1 < turns.length; i += 2) {
		const [prompt, reply] = [turns[i], turns[i + 1]];
		if (
			prompt.role === "user" &&
			reply.role === "assistant" &&
			(reply.status === "done" || reply.status === "stopped") &&
			reply.content
		) {
			messages.push(
				{ role: "user", content: prompt.content },
				{ role: "assistant", content: reply.content },
			);
		}
	}
	return messages;
}

/**
 * A multi-turn chat with one model, streamed through the BFF. Each reply
 * records time to first token, total time and usage, and ends as done,
 * stopped (by the user) or error.
 */
export function useChat(model: string) {
	const [turns, setTurns] = useState<ChatTurn[]>([]);
	const turnsRef = useRef(turns);
	turnsRef.current = turns;
	const controller = useRef<AbortController | null>(null);

	// Leaving the page stops the request, which the BFF cancels upstream.
	useEffect(() => () => controller.current?.abort(), []);

	const updateReply = useCallback(
		(id: string, patch: (turn: AssistantTurn) => AssistantTurn) =>
			setTurns((all) =>
				all.map((turn) =>
					turn.id === id && turn.role === "assistant" ? patch(turn) : turn,
				),
			),
		[],
	);

	const send = useCallback(
		async (text: string) => {
			const prompt = text.trim();
			if (!prompt || controller.current) return;
			const abort = new AbortController();
			controller.current = abort;
			const messages: ChatMessage[] = [
				...toMessages(turnsRef.current),
				{ role: "user", content: prompt },
			];
			const replyId = nextId();
			setTurns((all) => [
				...all,
				{ id: nextId(), role: "user", content: prompt },
				{
					id: replyId,
					role: "assistant",
					content: "",
					reasoning: "",
					status: "streaming",
				},
			]);

			const started = performance.now();
			const elapsed = () => performance.now() - started;
			try {
				const stream = streamChatCompletion(
					{ model, messages, stream_options: { include_usage: true } },
					abort.signal,
				);
				for await (const chunk of stream) {
					const delta = chunk.choices[0]?.delta;
					const reasoning = delta?.reasoning_content ?? "";
					const content = delta?.content ?? "";
					const usage = chunk.usage ?? undefined;
					if (!reasoning && !content && !usage) continue;
					const at = elapsed();
					updateReply(replyId, (turn) => ({
						...turn,
						reasoning: turn.reasoning + reasoning,
						content: turn.content + content,
						usage: usage ?? turn.usage,
						firstTokenMs:
							turn.firstTokenMs ?? (reasoning || content ? at : undefined),
						answerStartMs: turn.answerStartMs ?? (content ? at : undefined),
					}));
				}
				updateReply(replyId, (turn) => ({
					...turn,
					status: "done",
					totalMs: elapsed(),
				}));
			} catch (error) {
				const totalMs = elapsed();
				updateReply(replyId, (turn) =>
					abort.signal.aborted
						? { ...turn, status: "stopped", totalMs }
						: {
								...turn,
								status: "error",
								error: describeChatError(error),
								totalMs,
							},
				);
			} finally {
				if (controller.current === abort) controller.current = null;
			}
		},
		[model, updateReply],
	);

	const stop = useCallback(() => controller.current?.abort(), []);

	const reset = useCallback(() => {
		controller.current?.abort();
		controller.current = null;
		setTurns([]);
	}, []);

	/** Sends the last prompt again after its reply failed. */
	const retry = useCallback(() => {
		const all = turnsRef.current;
		const [prompt, reply] = all.slice(-2);
		if (prompt?.role !== "user" || reply?.role !== "assistant") return;
		turnsRef.current = all.slice(0, -2);
		setTurns(turnsRef.current);
		void send(prompt.content);
	}, [send]);

	const last = turns.at(-1);
	const streaming = last?.role === "assistant" && last.status === "streaming";

	return { turns, streaming, send, stop, reset, retry };
}
