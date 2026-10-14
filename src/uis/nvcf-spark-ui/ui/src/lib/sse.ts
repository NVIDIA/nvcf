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

import { getCreateChatCompletionUrl } from "~/generated/api/chat/chat";
import type { ChatCompletionChunk } from "~/generated/model/chatCompletionChunk";
import type { ChatCompletionRequest } from "~/generated/model/chatCompletionRequest";
import { errorMessage, HttpError } from "./fetch";

/** The stream carried an error event, which some backends send mid-stream. */
export class StreamError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "StreamError";
	}
}

/**
 * The stream ended without `data: [DONE]`. The gateway ends the stream that way
 * when the backend fails after streaming has started, so the response is
 * incomplete.
 */
export class StreamCutOffError extends Error {
	constructor() {
		super("The stream ended before the response was complete");
		this.name = "StreamCutOffError";
	}
}

/**
 * Streams a chat completion through the BFF and yields each chunk as it
 * arrives. This is the one call that doesn't go through the generated hooks:
 * they buffer the whole response, and this has to render tokens
 * as they stream.
 *
 * Throws {@link HttpError} when the request is rejected, {@link StreamError}
 * when the stream carries an error event, and {@link StreamCutOffError} when it
 * ends without `[DONE]`. Aborting `signal` stops the request; the BFF then
 * cancels it upstream.
 */
export async function* streamChatCompletion(
	request: ChatCompletionRequest,
	signal?: AbortSignal,
): AsyncGenerator<ChatCompletionChunk> {
	const res = await fetch(getCreateChatCompletionUrl(), {
		method: "POST",
		headers: {
			Accept: "text/event-stream",
			"Content-Type": "application/json",
		},
		body: JSON.stringify({ ...request, stream: true }),
		signal,
	});
	if (!res.ok) {
		const body = await res.json().catch(() => null);
		throw new HttpError(res, body);
	}
	if (!res.body) throw new StreamCutOffError();

	for await (const data of readEvents(res.body, signal)) {
		if (data === "[DONE]") return;
		const parsed: unknown = JSON.parse(data);
		if (typeof parsed === "object" && parsed !== null && "error" in parsed) {
			throw new StreamError(
				errorMessage(parsed) ?? "The model returned an error",
			);
		}
		yield parsed as ChatCompletionChunk;
	}
	throw new StreamCutOffError();
}

/**
 * Parses a server-sent event stream and yields the data of each event, with
 * multi-line data joined by `\n`. Follows the WHATWG parsing rules the
 * gateway relies on: any line ending, comment lines, and an event that only
 * dispatches at a blank line, so a trailing event without one is dropped.
 *
 * Aborting `signal` ends a pending read and throws the abort reason, even for
 * a body that doesn't watch the signal itself.
 */
export async function* readEvents(
	body: ReadableStream<Uint8Array>,
	signal?: AbortSignal,
): AsyncGenerator<string> {
	const reader = body.getReader();
	const onAbort = () => void reader.cancel(signal?.reason).catch(() => {});
	signal?.addEventListener("abort", onAbort, { once: true });
	const decoder = new TextDecoder();
	let buffer = "";
	let data: string[] = [];
	try {
		for (;;) {
			signal?.throwIfAborted();
			const { value, done } = await reader.read();
			signal?.throwIfAborted();
			if (done) return;
			// `stream` holds back a multi-byte character split across reads.
			buffer += decoder.decode(value, { stream: true });
			for (;;) {
				const end = buffer.search(/[\r\n]/);
				// A trailing \r may be the first half of \r\n; wait for the next read.
				if (end === -1 || (buffer[end] === "\r" && end === buffer.length - 1)) {
					break;
				}
				const line = buffer.slice(0, end);
				buffer = buffer.slice(
					buffer.startsWith("\r\n", end) ? end + 2 : end + 1,
				);
				if (line === "") {
					if (data.length > 0) yield data.join("\n");
					data = [];
					continue;
				}
				if (line.startsWith(":")) continue;
				const colon = line.indexOf(":");
				const field = colon === -1 ? line : line.slice(0, colon);
				if (field !== "data") continue;
				const raw = colon === -1 ? "" : line.slice(colon + 1);
				data.push(raw.startsWith(" ") ? raw.slice(1) : raw);
			}
		}
	} finally {
		signal?.removeEventListener("abort", onAbort);
		// Releases the connection when the consumer stops early.
		await reader.cancel().catch(() => {});
	}
}
