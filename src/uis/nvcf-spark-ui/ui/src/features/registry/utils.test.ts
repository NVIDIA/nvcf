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

import { describe, expect, it } from "vitest";
import { HttpError } from "~/lib/fetch";
import {
	chatCurl,
	gatewayApiBase,
	gatewayHost,
	grafanaModelUrl,
	isGrafanaPath,
	isUnreachable,
	stackStatus,
} from "./utils";

function httpError(status: number) {
	return new HttpError(new Response(null, { status }), null);
}

describe("gatewayApiBase", () => {
	it("appends /v1 to the public gateway URL", () => {
		expect(gatewayApiBase("https://gw.example.com")).toBe(
			"https://gw.example.com/v1",
		);
	});

	it("drops trailing slashes", () => {
		expect(gatewayApiBase("https://gw.example.com//")).toBe(
			"https://gw.example.com/v1",
		);
	});

	it("falls back to a placeholder when no URL is configured", () => {
		expect(gatewayApiBase(undefined)).toBe("$GW_URL/v1");
	});
});

describe("gatewayHost", () => {
	it("returns host and port", () => {
		expect(gatewayHost("https://gw.example.com:8080/base")).toBe(
			"gw.example.com:8080",
		);
	});

	it("returns undefined for a missing or invalid URL", () => {
		expect(gatewayHost(undefined)).toBeUndefined();
		expect(gatewayHost("not a url")).toBeUndefined();
	});
});

describe("chatCurl", () => {
	it("builds a runnable request with a key placeholder", () => {
		expect(chatCurl("https://gw.example.com/v1", "GLM-5.3-UD-IQ2_M")).toBe(
			[
				`curl "https://gw.example.com/v1/chat/completions" \\`,
				`  -H "Authorization: Bearer $GW_KEY" \\`,
				`  -H "Content-Type: application/json" \\`,
				`  -d '{`,
				`    "model": "GLM-5.3-UD-IQ2_M",`,
				`    "messages": [{"role": "user", "content": "Hello"}]`,
				`  }'`,
			].join("\n"),
		);
	});

	it("keeps a model name with a slash intact", () => {
		expect(chatCurl("$GW_URL/v1", "meta/llama-3.1-8b-instruct")).toContain(
			`"model": "meta/llama-3.1-8b-instruct"`,
		);
	});

	it("escapes quotes so the body survives the shell", () => {
		const curl = chatCurl("$GW_URL/v1", `it's "odd"`);
		expect(curl).toContain(`"model": "it'\\''s \\"odd\\"",`);
	});
});

describe("grafanaModelUrl", () => {
	it("filters the dashboard to the model", () => {
		expect(
			grafanaModelUrl(
				"https://grafana.example.com/d/llm-demo?orgId=1",
				"meta/llama-3.1-8b-instruct",
			),
		).toBe(
			"https://grafana.example.com/d/llm-demo?orgId=1&var-model=meta%2Fllama-3.1-8b-instruct",
		);
	});

	it("resolves a root-relative URL against the UI's own origin", () => {
		expect(grafanaModelUrl("/grafana/d/llm-demo", "GLM-5.3-UD-IQ2_M")).toBe(
			`${window.location.origin}/grafana/d/llm-demo?var-model=GLM-5.3-UD-IQ2_M`,
		);
	});

	it("returns undefined for an invalid URL", () => {
		expect(grafanaModelUrl("http://[::1", "m")).toBeUndefined();
	});
});

describe("isGrafanaPath", () => {
	it("matches the dashboard behind the UI's own ingress", () => {
		expect(isGrafanaPath("/grafana/d/llm-demo", "/grafana/d/llm-demo")).toBe(
			true,
		);
	});

	it("matches the slug Grafana appends, and ignores trailing slashes", () => {
		expect(
			isGrafanaPath("/grafana/d/llm-demo/llm-demo", "/grafana/d/llm-demo/"),
		).toBe(true);
		expect(isGrafanaPath("/grafana/d/llm-demo/", "/grafana/d/llm-demo")).toBe(
			true,
		);
	});

	it("matches an absolute URL on the UI's own origin", () => {
		expect(
			isGrafanaPath(
				"/grafana/d/llm-demo",
				`${window.location.origin}/grafana/d/llm-demo?orgId=1`,
			),
		).toBe(true);
	});

	it("ignores other paths, even ones that share a prefix", () => {
		expect(isGrafanaPath("/grafana/d/other", "/grafana/d/llm-demo")).toBe(
			false,
		);
		expect(isGrafanaPath("/grafana/d/llm-demo2", "/grafana/d/llm-demo")).toBe(
			false,
		);
	});

	it("never matches a Grafana on another origin, or none at all", () => {
		expect(
			isGrafanaPath("/d/llm-demo", "https://grafana.example.com/d/llm-demo"),
		).toBe(false);
		expect(isGrafanaPath("/grafana/d/llm-demo", undefined)).toBe(false);
		expect(isGrafanaPath("/", "/")).toBe(false);
		expect(isGrafanaPath("/grafana", "http://[::1")).toBe(false);
	});
});

describe("stackStatus", () => {
	it.each([
		["a 502", httpError(502), "unreachable"],
		["a 504", httpError(504), "unreachable"],
		["a network error", new TypeError("Failed to fetch"), "unreachable"],
		["a 500", httpError(500), "error"],
		["a 401", httpError(401), "error"],
		["an unparsable body", new SyntaxError("Unexpected token"), "error"],
	])("is %s → %s", (_, error, want) => {
		expect(stackStatus(error, true)).toBe(want);
		expect(isUnreachable(error)).toBe(want === "unreachable");
	});

	it("is connected once the registry has loaded without error", () => {
		expect(stackStatus(null, true)).toBe("connected");
	});

	it("is connecting before the first response", () => {
		expect(stackStatus(null, false)).toBe("connecting");
	});
});
