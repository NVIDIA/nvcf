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

import { afterEach, describe, expect, it, vi } from "vitest";
import { copyToClipboard } from "./clipboard";

describe("copyToClipboard", () => {
	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("writes via navigator.clipboard in a secure context", async () => {
		await copyToClipboard("my-function-id");

		expect(await navigator.clipboard.readText()).toBe("my-function-id");
	});

	it("falls back to execCommand when navigator.clipboard rejects", async () => {
		// Simulate an insecure (HTTP) context where writeText is unavailable.
		vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(
			new Error("insecure context"),
		);
		const execCommand = vi.fn(() => true);
		document.execCommand = execCommand;

		await copyToClipboard("my-function-id");

		expect(execCommand).toHaveBeenCalledWith("copy");
		// The temporary textarea is cleaned up after copying.
		expect(document.querySelector("textarea")).toBeNull();
	});
});
