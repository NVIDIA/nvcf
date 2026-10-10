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
import { formatClock, formatDuration } from "./time";

describe("formatClock", () => {
	it("shows the local time of day to the second", () => {
		const ms = new Date(2026, 9, 7, 14, 3, 12).getTime();

		expect(formatClock(ms)).toMatch(/\b0?2:03:12\b|\b14:03:12\b/);
	});
});

describe("formatDuration", () => {
	it.each([
		[0, "0 ms"],
		[680.4, "680 ms"],
		[999, "999 ms"],
		[1_000, "1.0 s"],
		[3_240, "3.2 s"],
	])("formats %d ms as %s", (ms, want) => {
		expect(formatDuration(ms)).toBe(want);
	});
});
