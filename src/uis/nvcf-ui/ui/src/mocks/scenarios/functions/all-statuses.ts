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

import {
	getGetAllFunctionsMockHandler,
	getGetFunctionVersionResponseMock,
} from "~/generated/api/function-management/function-management.msw";
import type { FunctionDto } from "~/generated/model/functionDto";

const STATUSES = [
	"ACTIVE",
	"DEPLOYING",
	"ERROR",
	"INACTIVE",
	"DELETED",
	"DEGRADED",
	"DEGRADING",
] as const;

// One function per status — useful for verifying all status badge variants at once.
const FUNCTIONS: FunctionDto[] = STATUSES.map((status) => ({
	...getGetFunctionVersionResponseMock().function,
	status,
}));

export const handlers = [
	getGetAllFunctionsMockHandler({ functions: FUNCTIONS }),
];
