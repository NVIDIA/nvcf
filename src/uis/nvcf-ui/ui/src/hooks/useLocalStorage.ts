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

import { useCallback, useMemo, useSyncExternalStore } from "react";

// Same-tab writes notify via a privately-named event so they don't pollute the
// native `storage` channel; cross-tab writes arrive on `storage` itself.
const SYNC_EVENT = "nvcf-local-storage";

function parse<T>(raw: string | null, fallback: T): T {
	if (raw === null) return fallback;
	try {
		return JSON.parse(raw) as T;
	} catch {
		// Not JSON (e.g. a plain string written elsewhere) — return it as-is.
		return raw as T;
	}
}

/** Read a stored value outside React (e.g. a route `beforeLoad`). */
export function readLocalStorage<T>(key: string, fallback: T): T {
	return parse(localStorage.getItem(key), fallback);
}

export function useLocalStorage<T>(key: string, fallback: T) {
	const subscribe = useCallback(
		(onChange: () => void) => {
			const onStorage = (event: StorageEvent) => {
				if (event.key === key) onChange();
			};
			const onSync = (event: Event) => {
				if ((event as CustomEvent<string>).detail === key) onChange();
			};
			window.addEventListener("storage", onStorage);
			window.addEventListener(SYNC_EVENT, onSync);
			return () => {
				window.removeEventListener("storage", onStorage);
				window.removeEventListener(SYNC_EVENT, onSync);
			};
		},
		[key],
	);

	// Snapshot is the raw string — a primitive, so it's referentially stable and
	// won't trigger an infinite loop. Parse into the typed value in a memo.
	const raw = useSyncExternalStore(subscribe, () => localStorage.getItem(key));
	const value = useMemo(() => parse(raw, fallback), [raw, fallback]);

	const setValue = useCallback(
		(next: T) => {
			localStorage.setItem(key, JSON.stringify(next));
			window.dispatchEvent(new CustomEvent(SYNC_EVENT, { detail: key }));
		},
		[key],
	);

	return [value, setValue] as const;
}
