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
import { type AssistantTurn, type ChatTurn, toMessages } from "./useChat";

const prompt = (content: string): ChatTurn => ({
	id: content,
	role: "user",
	content,
});

const reply = (
	content: string,
	status: AssistantTurn["status"] = "done",
): ChatTurn => ({
	id: `re: ${content}`,
	role: "assistant",
	content,
	reasoning: "thinking that isn't sent back",
	status,
});

describe("toMessages", () => {
	it("returns completed exchanges, without the reasoning", () => {
		expect(toMessages([prompt("Hi"), reply("Hello")])).toEqual([
			{ role: "user", content: "Hi" },
			{ role: "assistant", content: "Hello" },
		]);
	});

	it("keeps a stopped reply's partial answer", () => {
		expect(toMessages([prompt("Hi"), reply("Hel", "stopped")])).toEqual([
			{ role: "user", content: "Hi" },
			{ role: "assistant", content: "Hel" },
		]);
	});

	it.each([
		["failed", reply("", "error")],
		["still streaming", reply("Hel", "streaming")],
		["stopped before any answer", reply("", "stopped")],
	])("leaves out an exchange whose reply %s, so roles alternate", (_, last) => {
		expect(toMessages([prompt("A"), reply("a"), prompt("B"), last])).toEqual([
			{ role: "user", content: "A" },
			{ role: "assistant", content: "a" },
		]);
	});

	it("is empty for an empty conversation", () => {
		expect(toMessages([])).toEqual([]);
	});
});
