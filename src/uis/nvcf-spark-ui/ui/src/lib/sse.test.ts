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

import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import type { ChatCompletionChunk } from "~/generated/model/chatCompletionChunk";
import { server } from "~/mocks/server";
import { HttpError } from "./fetch";
import {
	readEvents,
	StreamCutOffError,
	StreamError,
	streamChatCompletion,
} from "./sse";

/** A byte stream that delivers `parts` as separate reads. */
function streamOf(...parts: (string | Uint8Array)[]) {
	const encoder = new TextEncoder();
	return new ReadableStream<Uint8Array>({
		start(controller) {
			for (const part of parts) {
				controller.enqueue(
					typeof part === "string" ? encoder.encode(part) : part,
				);
			}
			controller.close();
		},
	});
}

async function collect(stream: ReadableStream<Uint8Array>) {
	const events: string[] = [];
	for await (const data of readEvents(stream)) events.push(data);
	return events;
}

describe("readEvents", () => {
	it("yields the data of each event", async () => {
		expect(await collect(streamOf("data: a\n\ndata: b\n\n"))).toEqual([
			"a",
			"b",
		]);
	});

	it("reassembles events split across reads", async () => {
		expect(
			await collect(streamOf("da", "ta: hel", "lo\n", "\ndata: x\n\n")),
		).toEqual(["hello", "x"]);
	});

	it("accepts CRLF and CR line endings, including CRLF split across reads", async () => {
		expect(
			await collect(streamOf("data: a\r", "\n\r\ndata: b\r\rdata: c\n\n")),
		).toEqual(["a", "b", "c"]);
	});

	it("joins multi-line data with newlines", async () => {
		expect(await collect(streamOf("data: one\ndata: two\n\n"))).toEqual([
			"one\ntwo",
		]);
	});

	it("skips comments, other fields and empty events", async () => {
		expect(
			await collect(
				streamOf(": keep-alive\n\nevent: chunk\nid: 7\nretry: 10\n\ndata\n\n"),
			),
		).toEqual([""]);
	});

	it("keeps a value without the optional space after the colon", async () => {
		expect(await collect(streamOf("data:x\n\n"))).toEqual(["x"]);
	});

	it("drops a trailing event that never ends with a blank line", async () => {
		expect(await collect(streamOf("data: a\n\ndata: partial"))).toEqual(["a"]);
	});

	it("decodes a multi-byte character split across reads", async () => {
		const bytes = new TextEncoder().encode("data: 17 × 19\n\n");
		const split = bytes.indexOf(0xd7);
		expect(
			await collect(streamOf(bytes.slice(0, split), bytes.slice(split))),
		).toEqual(["17 × 19"]);
	});

	it("stops waiting on a stream that never ends when aborted", async () => {
		const abort = new AbortController();
		const silent = new ReadableStream<Uint8Array>({
			start(controller) {
				controller.enqueue(new TextEncoder().encode("data: a\n\n"));
				// Never closes, and doesn't watch the signal.
			},
		});
		const events: string[] = [];

		const read = (async () => {
			for await (const data of readEvents(silent, abort.signal)) {
				events.push(data);
				abort.abort();
			}
		})();

		await expect(read).rejects.toHaveProperty("name", "AbortError");
		expect(events).toEqual(["a"]);
	});

	it("cancels the stream when the consumer stops early", async () => {
		let cancelled = false;
		const stream = new ReadableStream<Uint8Array>({
			start(controller) {
				controller.enqueue(new TextEncoder().encode("data: a\n\ndata: b\n\n"));
			},
			cancel() {
				cancelled = true;
			},
		});
		for await (const _ of readEvents(stream)) break;
		expect(cancelled).toBe(true);
	});
});

function chunk(content: string): ChatCompletionChunk {
	return {
		id: "c",
		object: "chat.completion.chunk",
		created: 1,
		model: "m",
		choices: [{ index: 0, delta: { content }, finish_reason: null }],
	};
}

function sse(...events: string[]) {
	return new HttpResponse(streamOf(...events.map((e) => `data: ${e}\n\n`)), {
		headers: { "Content-Type": "text/event-stream" },
	});
}

const request = {
	model: "m",
	messages: [{ role: "user" as const, content: "hi" }],
};

async function stream() {
	const chunks: ChatCompletionChunk[] = [];
	for await (const c of streamChatCompletion(request)) chunks.push(c);
	return chunks;
}

describe("streamChatCompletion", () => {
	it("posts a streaming request and yields chunks until [DONE]", async () => {
		let body: unknown;
		server.use(
			http.post("/v1/chat/completions", async ({ request: req }) => {
				body = await req.json();
				return sse(
					JSON.stringify(chunk("Hel")),
					JSON.stringify(chunk("lo")),
					"[DONE]",
				);
			}),
		);

		const chunks = await stream();

		expect(chunks.map((c) => c.choices[0]?.delta.content)).toEqual([
			"Hel",
			"lo",
		]);
		expect(body).toEqual({ ...request, stream: true });
	});

	it("throws HttpError with the gateway's error body", async () => {
		server.use(
			http.post("/v1/chat/completions", () =>
				HttpResponse.json(
					{
						error: {
							code: "invalid_api_key",
							message: "Missing or invalid API key",
						},
					},
					{ status: 401 },
				),
			),
		);

		const error = await stream().catch((e: unknown) => e);

		expect(error).toBeInstanceOf(HttpError);
		expect(error).toMatchObject({
			status: 401,
			body: { error: { code: "invalid_api_key" } },
		});
	});

	it("throws HttpError with no body when the error isn't JSON", async () => {
		server.use(
			http.post(
				"/v1/chat/completions",
				() => new HttpResponse("upstream connect error", { status: 503 }),
			),
		);

		await expect(stream()).rejects.toMatchObject({ status: 503, body: null });
	});

	it("throws StreamError for an error event in the stream", async () => {
		server.use(
			http.post("/v1/chat/completions", () =>
				sse(
					JSON.stringify(chunk("Hi")),
					JSON.stringify({ error: { message: "CUDA out of memory" } }),
				),
			),
		);

		const error = await stream().catch((e: unknown) => e);

		expect(error).toBeInstanceOf(StreamError);
		expect(error).toHaveProperty("message", "CUDA out of memory");
	});

	it("throws StreamError with a fallback for an error event without a message", async () => {
		server.use(
			http.post("/v1/chat/completions", () =>
				sse(JSON.stringify({ error: {} })),
			),
		);

		await expect(stream()).rejects.toThrow("The model returned an error");
	});

	it("throws StreamCutOffError when the stream ends without [DONE]", async () => {
		server.use(
			http.post("/v1/chat/completions", () => sse(JSON.stringify(chunk("Hi")))),
		);

		await expect(stream()).rejects.toBeInstanceOf(StreamCutOffError);
	});

	it("rejects when aborted", async () => {
		const abort = new AbortController();
		abort.abort();

		const consume = async () => {
			for await (const _ of streamChatCompletion(request, abort.signal)) {
				// unreachable
			}
		};

		await expect(consume()).rejects.toThrow();
	});
});
