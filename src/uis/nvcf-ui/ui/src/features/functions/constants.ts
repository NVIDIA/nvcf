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

import { FunctionDtoStatus } from "~/generated/model/functionDtoStatus";

export const functionStatusValues = Object.values(FunctionDtoStatus);

export const DEPLOYED_FUNCTION_STATUSES: FunctionDtoStatus[] = [
	FunctionDtoStatus.ACTIVE,
	FunctionDtoStatus.DEPLOYING,
	FunctionDtoStatus.DEGRADED,
	FunctionDtoStatus.DEGRADING,
	FunctionDtoStatus.ERROR,
];

export const FUNCTION_TYPE = {
	HELM: "Helm Function",
	CONTAINER: "Container Function",
} as const;
