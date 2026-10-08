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

/**
 * A text field mirrored to the URL: typing updates the value at once and
 * writes it to the URL `delay` ms after the last keystroke. A URL change from
 * elsewhere, such as back or forward, replaces what was typed; a write that
 * comes back as the URL's new value doesn't, so fast typing isn't undone.
 */
export function useUrlText(
	urlValue: string,
	write: (value: string) => void,
	delay = 300,
): [string, (value: string) => void] {
	const [value, setValue] = useState(urlValue);
	const written = useRef(urlValue);

	useEffect(() => {
		if (urlValue !== written.current) {
			written.current = urlValue;
			setValue(urlValue);
		}
	}, [urlValue]);

	useEffect(() => {
		if (value === written.current) return;
		const timer = setTimeout(() => {
			written.current = value;
			write(value);
		}, delay);
		return () => clearTimeout(timer);
	}, [value, write, delay]);

	return [value, setValue];
}
