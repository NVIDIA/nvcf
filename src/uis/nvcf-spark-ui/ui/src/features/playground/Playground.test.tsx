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

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { registryRoute } from "~/features/registry/routes";
import type { ChatCompletionRequest } from "~/generated/model/chatCompletionRequest";
import { handlers as busy } from "~/mocks/scenarios/playground/busy";
import { handlers as cutOff } from "~/mocks/scenarios/playground/cut-off";
import { handlers as chatStackDown } from "~/mocks/scenarios/playground/stack-down";
import { handlers as unauthorized } from "~/mocks/scenarios/playground/unauthorized";
import { handlers as registryDown } from "~/mocks/scenarios/registry/stack-down";
import { server } from "~/mocks/server";
import { renderWithRouter } from "~/testing/render";
import { playgroundRoute } from "./routes";
import { EXAMPLE_PROMPT } from "./utils";

const GLM = "GLM-5.3-UD-IQ2_M";

function renderPlayground(model?: string) {
	return renderWithRouter({
		routes: [registryRoute, playgroundRoute],
		initialLocation: model
			? `/playground?model=${encodeURIComponent(model)}`
			: "/playground",
	});
}

async function messageBox() {
	return screen.findByRole("textbox", { name: "Message" });
}

describe("Playground", () => {
	it("streams a reply with its reasoning, answer and stats", async () => {
		const user = userEvent.setup();
		renderPlayground(GLM);

		expect(
			await screen.findByRole("heading", { level: 1, name: GLM }),
		).toBeInTheDocument();
		const box = await messageBox();
		expect(box).toHaveValue(EXAMPLE_PROMPT);

		await user.click(screen.getByRole("button", { name: "Send" }));

		expect(box).toHaveValue("");
		expect(screen.getByText(EXAMPLE_PROMPT)).toBeInTheDocument();
		expect(
			await screen.findByText(/^Latency is how long one request takes/),
		).toBeInTheDocument();
		expect(
			await screen.findByText(/\d+ tokens · first token/),
		).toBeInTheDocument();
		expect(screen.getByText(/^Thought for/)).toBeInTheDocument();
		expect(screen.getByText("Reply complete")).toBeInTheDocument();
	});

	it("shows the reasoning when expanded", async () => {
		const user = userEvent.setup();
		renderPlayground(GLM);
		await user.click(await screen.findByRole("button", { name: "Send" }));

		await user.click(await screen.findByText(/^Thought for/));

		expect(screen.getByText(/contrasted for model serving/)).toBeVisible();
	});

	it("sends with Enter and adds a line with Shift+Enter", async () => {
		const user = userEvent.setup();
		renderPlayground(GLM);
		const box = await messageBox();

		await user.clear(box);
		await user.type(box, "Hello{Shift>}{Enter}{/Shift}there");
		expect(box).toHaveValue("Hello\nthere");

		await user.keyboard("{Enter}");

		expect(box).toHaveValue("");
		expect(
			await screen.findByText(/^This reply comes from the mock gateway/),
		).toBeInTheDocument();
	});

	it("sends the conversation so far with each message", async () => {
		const user = userEvent.setup();
		const bodies: ChatCompletionRequest[] = [];
		server.use(
			http.post("/v1/chat/completions", async ({ request }) => {
				bodies.push((await request.clone().json()) as ChatCompletionRequest);
			}),
		);
		renderPlayground(GLM);
		const box = await messageBox();

		await user.click(screen.getByRole("button", { name: "Send" }));
		await screen.findByText("Reply complete");
		await user.type(box, "And in one sentence?{Enter}");
		await waitFor(() => expect(bodies).toHaveLength(2));

		expect(bodies[1]).toMatchObject({
			model: GLM,
			stream: true,
			stream_options: { include_usage: true },
		});
		expect(bodies[1]?.messages.map((m) => m.role)).toEqual([
			"user",
			"assistant",
			"user",
		]);
		expect(bodies[1]?.messages[2]).toEqual({
			role: "user",
			content: "And in one sentence?",
		});
	});

	it("stops a reply", async () => {
		const user = userEvent.setup();
		const encoder = new TextEncoder();
		server.use(
			http.post(
				"/v1/chat/completions",
				() =>
					new HttpResponse(
						new ReadableStream({
							start(controller) {
								controller.enqueue(
									encoder.encode(
										`data: ${JSON.stringify({
											id: "c",
											object: "chat.completion.chunk",
											created: 1,
											model: GLM,
											choices: [{ index: 0, delta: { content: "Partial" } }],
										})}\n\n`,
									),
								);
								// Never closes: the model is still writing.
							},
						}),
						{ headers: { "Content-Type": "text/event-stream" } },
					),
			),
		);
		renderPlayground(GLM);
		await user.click(await screen.findByRole("button", { name: "Send" }));
		expect(await screen.findByText("Partial")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Stop" }));

		expect(await screen.findByText(/^Stopped/)).toBeInTheDocument();
		expect(screen.getByText("Reply stopped")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
	});

	it.each([
		[
			"the UI's key is rejected",
			unauthorized,
			"The gateway rejected this UI's key.",
		],
		["the gateway is unreachable", chatStackDown, "Gateway unreachable."],
		["the model is busy", busy, "The model is busy."],
		["the stream is cut off", cutOff, "The response was cut off."],
	])("explains when %s", async (_, handlers, message) => {
		const user = userEvent.setup();
		server.use(...handlers);
		renderPlayground(GLM);

		await user.click(await screen.findByRole("button", { name: "Send" }));

		// The banner reads "{title}. {detail}"; the live region has the title alone.
		expect(
			await screen.findByText(message, { exact: false }),
		).toBeInTheDocument();
	});

	it("keeps what streamed before the cut-off", async () => {
		const user = userEvent.setup();
		server.use(...cutOff);
		renderPlayground(GLM);

		await user.click(await screen.findByRole("button", { name: "Send" }));

		expect(
			await screen.findByText(/The response was cut off\./),
		).toBeInTheDocument();
		expect(screen.getByText(/^Latency is how long one/)).toBeInTheDocument();
	});

	it("retries a failed reply", async () => {
		const user = userEvent.setup();
		server.use(
			http.post(
				"/v1/chat/completions",
				() =>
					HttpResponse.json(
						{ error: { code: "overloaded_error", message: "busy" } },
						{ status: 529 },
					),
				{ once: true },
			),
		);
		renderPlayground(GLM);
		await user.click(await screen.findByRole("button", { name: "Send" }));
		await screen.findByText(/The model is busy\./);

		await user.click(screen.getByRole("button", { name: "Try again" }));

		expect(
			await screen.findByText(/^Latency is how long one request takes/),
		).toBeInTheDocument();
		expect(screen.queryByText(/The model is busy\./)).not.toBeInTheDocument();
		expect(screen.getAllByText(EXAMPLE_PROMPT)).toHaveLength(1);
	});

	it("starts over with New chat", async () => {
		const user = userEvent.setup();
		renderPlayground(GLM);
		await user.click(await screen.findByRole("button", { name: "Send" }));
		await screen.findByText("Reply complete");

		await user.click(screen.getByRole("button", { name: "New chat" }));

		expect(
			screen.getByRole("heading", { level: 2, name: `Try ${GLM}` }),
		).toBeInTheDocument();
		expect(screen.queryByText(/^Latency is how long/)).not.toBeInTheDocument();
	});

	it("blocks chat with an unhealthy model", async () => {
		renderPlayground("deepseek-ai/deepseek-v4-flash");

		expect(
			await screen.findByText(/This endpoint is unhealthy/),
		).toBeInTheDocument();
		expect(await messageBox()).toBeDisabled();
		expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
		expect(
			screen.getByRole("link", { name: "View in registry" }),
		).toHaveAttribute(
			"href",
			"/registry?model=deepseek-ai%2Fdeepseek-v4-flash",
		);
	});

	it("blocks chat with a model that isn't registered", async () => {
		renderPlayground("gone/model");

		expect(
			await screen.findByText(/This model isn't registered with the gateway/),
		).toBeInTheDocument();
		expect(await messageBox()).toBeDisabled();
	});

	it("asks for a model when none is given", async () => {
		renderPlayground();

		expect(
			await screen.findByRole("heading", { name: "No model selected" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Go to endpoint registry" }),
		).toHaveAttribute("href", "/registry");
	});

	it("shows the stack-down state when the registry can't load", async () => {
		server.use(...registryDown);
		renderPlayground(GLM);

		expect(
			await screen.findByRole("heading", { name: "Gateway unreachable" }),
		).toBeInTheDocument();
	});
});

describe("untrusted model output", () => {
	it("renders markup in a reply as text", async () => {
		const user = userEvent.setup();
		const encoder = new TextEncoder();
		const chunk = (delta: object) =>
			`data: ${JSON.stringify({ id: "c", object: "chat.completion.chunk", created: 1, model: GLM, choices: [{ index: 0, delta }] })}\n\n`;
		server.use(
			http.post(
				"/v1/chat/completions",
				() =>
					new HttpResponse(
						new ReadableStream({
							start(controller) {
								controller.enqueue(
									encoder.encode(
										chunk({ reasoning_content: "<b>bold?</b>" }) +
											chunk({ content: `<img src=x onerror="alert(1)">` }) +
											"data: [DONE]\n\n",
									),
								);
								controller.close();
							},
						}),
						{ headers: { "Content-Type": "text/event-stream" } },
					),
			),
		);
		renderPlayground(GLM);
		await user.click(await screen.findByRole("button", { name: "Send" }));

		expect(
			await screen.findByText(`<img src=x onerror="alert(1)">`),
		).toBeInTheDocument();
		expect(document.querySelector("img")).toBeNull();
		expect(document.querySelector("b")).toBeNull();
	});
});
