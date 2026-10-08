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

import { useMemo } from "react";
import { useGetRegistry } from "~/generated/api/registry/registry";
import type { RegistryHealth } from "~/generated/model/registryHealth";

/**
 * Health by served model name, for every model registered with the gateway.
 * Reads the registry query the shell polls, without polling of its own, and is
 * empty while the registry hasn't loaded or the stack is down: the recipes
 * don't depend on the stack.
 */
export function useRegisteredModels(): ReadonlyMap<string, RegistryHealth> {
	const { data } = useGetRegistry();
	return useMemo(
		() => new Map((data?.models ?? []).map((m) => [m.model, m.health])),
		[data],
	);
}
