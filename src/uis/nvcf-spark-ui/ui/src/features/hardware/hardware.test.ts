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
import { asHardwareClass, hardwareClass } from "./hardware";

describe("hardwareClass", () => {
	it.each([
		"NVIDIA-GB10",
		"NVIDIA GB10",
		"NVIDIA_GB10",
		"nvidia-gb10",
	])("reads %s as DGX Spark", (product) => {
		expect(hardwareClass(product)).toBe("DGX Spark");
	});

	it.each([
		"NVIDIA-GB300",
		"NVIDIA GB300",
	])("reads %s as DGX Station", (product) => {
		expect(hardwareClass(product)).toBe("DGX Station");
	});

	it("leaves other GPUs, and near misses, unclassified", () => {
		expect(hardwareClass("NVIDIA-B200")).toBeUndefined();
		expect(hardwareClass("NVIDIA-GB200")).toBeUndefined();
		expect(hardwareClass("NVIDIA-GB100")).toBeUndefined();
		expect(hardwareClass("")).toBeUndefined();
	});
});

describe("asHardwareClass", () => {
	it("accepts only a known class", () => {
		expect(asHardwareClass("DGX Spark")).toBe("DGX Spark");
		expect(asHardwareClass("dgx spark")).toBeUndefined();
		expect(asHardwareClass(1)).toBeUndefined();
		expect(asHardwareClass(undefined)).toBeUndefined();
	});
});
