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

import { useEffect, useRef, useState } from "react";
import type { RegistryHealth } from "~/generated/model/registryHealth";

/**
 * When each model's health last changed while this page was open (epoch ms),
 * by model name. A model whose health hasn't changed since the page loaded
 * has no entry: the registry doesn't say how long a state has lasted,
 * so the first state seen gets no time.
 *
 * Polls that change nothing don't touch it, so the times stay put.
 */
export function useHealthChanges(
	models: readonly { model: string; health: RegistryHealth }[],
): ReadonlyMap<string, number> {
	const seen = useRef(new Map<string, RegistryHealth>());
	const [changedAt, setChangedAt] = useState<ReadonlyMap<string, number>>(
		() => new Map(),
	);

	useEffect(() => {
		const changed: string[] = [];
		for (const { model, health } of models) {
			const before = seen.current.get(model);
			if (before !== undefined && before !== health) changed.push(model);
			seen.current.set(model, health);
		}
		if (changed.length === 0) return;
		const now = Date.now();
		setChangedAt((previous) => {
			const next = new Map(previous);
			for (const model of changed) next.set(model, now);
			return next;
		});
	}, [models]);

	return changedAt;
}
