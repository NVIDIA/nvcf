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
import { GetClusterResponseStatus } from "~/generated/model/getClusterResponseStatus";
import { getStatusLabel } from "./StatusBadge";

describe("getStatusLabel", () => {
	it("surfaces UNHEALTHY as the friendlier 'Error' label", () => {
		// Non-obvious mapping: the status enum is UNHEALTHY but users see "Error".
		expect(getStatusLabel(GetClusterResponseStatus.UNHEALTHY)).toBe("Error");
	});

	it("falls back to a title-cased status when there is no override", () => {
		expect(getStatusLabel(GetClusterResponseStatus.READY)).toBe("Ready");
	});
});
