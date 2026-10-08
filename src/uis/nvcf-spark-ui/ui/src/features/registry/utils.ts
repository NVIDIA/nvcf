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

import { HttpError } from "~/lib/fetch";

/** The only gateway route the demo serves for inference. */
export const CHAT_COMPLETIONS_PATH = "/chat/completions";

/**
 * The OpenAI base URL clients call: the public gateway address from
 * `/api/v1/config` plus `/v1`, or a `$GW_URL` placeholder when the deployment
 * doesn't set one.
 */
export function gatewayApiBase(gatewayUrl: string | undefined): string {
	return `${gatewayUrl ? gatewayUrl.replace(/\/+$/, "") : "$GW_URL"}/v1`;
}

/** The host to show for the public gateway address, if it is a valid URL. */
export function gatewayHost(
	gatewayUrl: string | undefined,
): string | undefined {
	if (!gatewayUrl) return undefined;
	try {
		return new URL(gatewayUrl).host;
	} catch {
		return undefined;
	}
}

/** Single-quotes `value` for a POSIX shell. */
function shellQuote(value: string): string {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

/**
 * A ready-to-run `curl` for one chat completion. The key stays a `$GW_KEY`
 * placeholder: the UI never holds or issues one.
 */
export function chatCurl(apiBase: string, model: string): string {
	const body = [
		"{",
		`    "model": ${JSON.stringify(model)},`,
		`    "messages": [{"role": "user", "content": "Hello"}]`,
		"  }",
	].join("\n");
	return [
		`curl "${apiBase}${CHAT_COMPLETIONS_PATH}" \\`,
		`  -H "Authorization: Bearer $GW_KEY" \\`,
		`  -H "Content-Type: application/json" \\`,
		`  -d ${shellQuote(body)}`,
	].join("\n");
}

/**
 * The Grafana dashboard filtered to one model through its `model` variable,
 * or undefined when the configured URL doesn't parse. A root-relative URL is a
 * Grafana behind the same ingress as the UI, so it resolves against the UI's
 * own origin.
 */
export function grafanaModelUrl(
	grafanaUrl: string,
	model: string,
): string | undefined {
	try {
		const url = new URL(grafanaUrl, window.location.origin);
		url.searchParams.set("var-model", model);
		return url.toString();
	} catch {
		return undefined;
	}
}

/**
 * Whether `pathname` is the configured Grafana dashboard on the UI's own
 * origin, with or without the slug Grafana appends to it. The UI only ever
 * sees that path when nothing else on the ingress serves it: Grafana isn't
 * deployed there, or isn't routed.
 */
export function isGrafanaPath(
	pathname: string,
	grafanaUrl: string | undefined,
): boolean {
	if (!grafanaUrl) return false;
	let dashboard: URL;
	try {
		dashboard = new URL(grafanaUrl, window.location.origin);
	} catch {
		return false;
	}
	if (dashboard.origin !== window.location.origin) return false;
	const base = dashboard.pathname.replace(/\/+$/, "");
	const path = pathname.replace(/\/+$/, "");
	return base !== "" && (path === base || path.startsWith(`${base}/`));
}

export type StackStatus = "connecting" | "connected" | "unreachable" | "error";

/**
 * Whether an error means the routing stack can't be reached: a `502` or `504`
 * from the BFF, or a network error before any response.
 */
export function isUnreachable(error: unknown): boolean {
	if (error instanceof HttpError) {
		return error.status === 502 || error.status === 504;
	}
	return error instanceof TypeError;
}

/**
 * The routing stack's status, from the latest registry poll: its error (null
 * when it succeeded) and whether the registry has loaded at least once.
 */
export function stackStatus(error: unknown, hasData: boolean): StackStatus {
	if (error) return isUnreachable(error) ? "unreachable" : "error";
	return hasData ? "connected" : "connecting";
}
