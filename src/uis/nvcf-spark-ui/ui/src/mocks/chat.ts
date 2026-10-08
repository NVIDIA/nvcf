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

import { delay, HttpResponse, http } from "msw";
import type { ChatCompletionChunk } from "~/generated/model/chatCompletionChunk";
import type { ChatCompletionChunkChoicesItemDelta } from "~/generated/model/chatCompletionChunkChoicesItemDelta";
import type { ChatCompletionRequest } from "~/generated/model/chatCompletionRequest";
import { RegistryHealth } from "~/generated/model/registryHealth";
import { mockStore } from "./store/store";

/**
 * Hand-written streaming chat mock:
 * Orval's generated mocks can't stream. It follows the gateway's wire format,
 * as the recipe client sees it with GLM:
 * - `delta.reasoning_content` tokens, then `delta.content` tokens
 * - a chunk with `finish_reason: "stop"`
 * - a `usage` chunk with empty `choices` (`stream_options.include_usage`)
 * - `data: [DONE]`
 * Tokens are deterministic, a few milliseconds apart.
 */

/** Pause between tokens. Tests stream as fast as the event loop allows. */
const TOKEN_DELAY_MS = import.meta.env.MODE === "test" ? 0 : 15;

const LATENCY_REASONING =
	"The user wants latency and throughput contrasted for model serving. Define each, then explain the batching trade-off between them.";
// Markdown, as real models answer: bold, a nested list and a table.
const LATENCY_ANSWER = [
	"Latency is how long one request takes end to end, measured per request. Throughput is how much work the endpoint completes per second, measured in requests or tokens.",
	"",
	"**Latency** shows up in two numbers:",
	"- **Time to first token**: how long until the reply starts streaming.",
	"- **Time per output token**: how fast the rest of it arrives.",
	"  - Long replies are dominated by this one.",
	"",
	"**Throughput** is what the endpoint sustains across every user, in requests or tokens per second.",
	"",
	"| | Latency | Throughput |",
	"| --- | --- | --- |",
	"| Measured per | request | second |",
	"| Improves with | smaller batches | bigger batches |",
	"",
	"They are traded against each other rather than improved together: batching more requests raises throughput and usually raises latency, because each request waits for the batch to fill.",
].join("\n");

interface ChatMockOptions {
	/**
	 * Ends the stream without `[DONE]` after this many answer tokens, the way
	 * the gateway ends it when the backend fails mid-stream.
	 */
	cutOffAfter?: number;
	/** Pause between tokens, for a reply slow enough to stop. */
	tokenDelayMs?: number;
}

/** The words of `text`, each with its trailing whitespace, as stream tokens. */
function tokens(text: string): string[] {
	return text.match(/\S+\s*/g) ?? [];
}

function lastUserText(request: ChatCompletionRequest): string {
	const message = request.messages.findLast((m) => m.role === "user");
	return typeof message?.content === "string" ? message.content : "";
}

function reply(model: string, prompt: string) {
	if (/latency/i.test(prompt) && /throughput/i.test(prompt)) {
		return { reasoning: LATENCY_REASONING, answer: LATENCY_ANSWER };
	}
	return {
		reasoning:
			"This is the mock gateway, so the reply is fixed text whatever the prompt says.",
		answer: `This reply comes from the mock gateway, not from ${model}. In mock mode every prompt gets this fixed text, streamed token by token, so the playground behaves as it does against a real model.`,
	};
}

export function chatCompletionHandler({
	cutOffAfter,
	tokenDelayMs = TOKEN_DELAY_MS,
}: ChatMockOptions = {}) {
	return http.post("/v1/chat/completions", async ({ request }) => {
		const body = (await request.json()) as ChatCompletionRequest;
		const entry = mockStore.registry.find((m) => m.model === body.model);
		if (entry?.health !== RegistryHealth.Healthy) {
			return HttpResponse.json(
				{
					error: {
						code: "model_not_found",
						message: `The model '${body.model}' does not exist`,
						param: "model",
						type: "invalid_request_error",
					},
				},
				{ status: 404 },
			);
		}

		const { reasoning, answer } = reply(body.model, lastUserText(body));
		const reasoningTokens = tokens(reasoning);
		const answerTokens = tokens(answer);
		const usage = {
			prompt_tokens: body.messages.reduce(
				(sum, m) =>
					sum + (typeof m.content === "string" ? tokens(m.content).length : 0),
				0,
			),
			completion_tokens: reasoningTokens.length + answerTokens.length,
			total_tokens: 0,
		};
		usage.total_tokens = usage.prompt_tokens + usage.completion_tokens;
		const id = "chatcmpl-mock";
		const created = Math.floor(Date.now() / 1000);

		if (!body.stream) {
			return HttpResponse.json({
				id,
				object: "chat.completion",
				created,
				model: body.model,
				choices: [
					{
						index: 0,
						message: {
							role: "assistant",
							content: answer,
							reasoning_content: reasoning,
						},
						finish_reason: "stop",
					},
				],
				usage,
			});
		}

		const chunk = (
			delta: ChatCompletionChunkChoicesItemDelta,
			finishReason: string | null = null,
		): ChatCompletionChunk => ({
			id,
			object: "chat.completion.chunk",
			created,
			model: body.model,
			choices: [{ index: 0, delta, finish_reason: finishReason }],
		});

		const encoder = new TextEncoder();
		let cancelled = false;
		const stream = new ReadableStream<Uint8Array>({
			async start(controller) {
				const send = (data: unknown) => {
					if (cancelled) return;
					const payload =
						typeof data === "string" ? data : JSON.stringify(data);
					controller.enqueue(encoder.encode(`data: ${payload}\n\n`));
				};
				for (const [i, token] of reasoningTokens.entries()) {
					await delay(tokenDelayMs);
					send(
						chunk(
							i === 0
								? { role: "assistant", reasoning_content: token }
								: { reasoning_content: token },
						),
					);
				}
				for (const [i, token] of answerTokens.entries()) {
					if (i === cutOffAfter) {
						if (!cancelled) controller.close();
						return;
					}
					await delay(tokenDelayMs);
					send(chunk({ content: token }));
				}
				send(chunk({}, "stop"));
				send({ ...chunk({}), choices: [], usage });
				send("[DONE]");
				if (!cancelled) controller.close();
			},
			cancel() {
				cancelled = true;
			},
		});

		return new HttpResponse(stream, {
			headers: {
				"Cache-Control": "no-cache",
				"Content-Type": "text/event-stream",
			},
		});
	});
}
