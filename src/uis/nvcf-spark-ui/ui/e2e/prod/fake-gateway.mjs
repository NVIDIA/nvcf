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

// A stand-in for the LLM API Gateway, for the suite that runs the production
// bundle behind the real BFF (`make e2e-prod`). Like the real gateway it
// answers only requests that carry the caller key, so the suite proves the BFF
// injects its key, and it streams chat completions the same way. It also
// rejects any request that still carries the browser's cookies, which the BFF
// must strip.
import { createServer } from "node:http";

const port = Number(process.env.PORT ?? 5299);
const key = process.env.KEY ?? "";

const registry = [
	{ model: "GLM-5.3-UD-IQ2_M", healthy: true },
	{ model: "deepseek-ai/deepseek-v4-flash", healthy: false },
	{ model: "meta/llama-3.1-8b-instruct", healthy: true },
];

function json(res, status, body) {
	res.writeHead(status, { "Content-Type": "application/json" });
	res.end(JSON.stringify(body));
}

function openAIError(res, status, code, message) {
	json(res, status, {
		error: { code, message, param: "", type: "invalid_request_error" },
	});
}

async function readJSON(req) {
	const chunks = [];
	for await (const chunk of req) chunks.push(chunk);
	return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function streamChat(req, res) {
	const request = await readJSON(req);
	const entry = registry.find((m) => m.model === request.model);
	if (!entry?.healthy) {
		openAIError(
			res,
			404,
			"model_not_found",
			`The model '${request.model}' does not exist`,
		);
		return;
	}
	res.writeHead(200, {
		"Content-Type": "text/event-stream",
		"Cache-Control": "no-cache",
	});
	const created = Math.floor(Date.now() / 1000);
	const send = (delta, extra = {}) =>
		res.write(
			`data: ${JSON.stringify({
				id: "chatcmpl-e2e",
				object: "chat.completion.chunk",
				created,
				model: request.model,
				choices: [{ index: 0, delta, finish_reason: null }],
				...extra,
			})}\n\n`,
		);
	const reasoning = ["Answer ", "briefly."];
	const answer = ["Served ", "through ", "the ", "BFF."];
	for (const [i, token] of reasoning.entries()) {
		await sleep(20);
		send(
			i === 0
				? { role: "assistant", reasoning_content: token }
				: { reasoning_content: token },
		);
	}
	for (const token of answer) {
		await sleep(20);
		send({ content: token });
	}
	send({}, { choices: [{ index: 0, delta: {}, finish_reason: "stop" }] });
	send(
		{},
		{
			choices: [],
			usage: { prompt_tokens: 3, completion_tokens: 6, total_tokens: 9 },
		},
	);
	res.end("data: [DONE]\n\n");
}

createServer(async (req, res) => {
	const url = new URL(req.url ?? "/", `http://${req.headers.host}`);
	if (url.pathname === "/healthz") {
		res.end("ok");
		return;
	}
	if (req.headers.authorization !== `Bearer ${key}`) {
		openAIError(res, 401, "invalid_api_key", "Missing or invalid API key");
		return;
	}
	if (req.headers.cookie) {
		json(res, 400, { message: "The browser's cookies reached the gateway" });
		return;
	}
	if (req.method === "GET" && url.pathname === "/v1/registry") {
		json(res, 200, {
			generatedAt: new Date().toISOString(),
			models: registry.map(({ model, healthy }) => ({
				model,
				health: healthy ? "Healthy" : "Unhealthy",
				clusters: [
					{
						clusterId: "e2e-cluster",
						registeredServers: 1,
						healthyServers: healthy ? 1 : 0,
					},
				],
			})),
		});
		return;
	}
	if (req.method === "POST" && url.pathname === "/v1/chat/completions") {
		await streamChat(req, res);
		return;
	}
	json(res, 404, { message: "Not Found" });
}).listen(port, "127.0.0.1");
