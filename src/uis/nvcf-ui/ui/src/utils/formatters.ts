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

import dayjs from "dayjs";
import { startCase, toLower } from "lodash-es";

// Strings

export function toTitleCase(value: string): string {
	return startCase(toLower(value));
}

// Numbers

export function formatNumberCompact(value: number): string {
	return new Intl.NumberFormat("en-US", {
		notation: "compact",
		compactDisplay: "short",
	}).format(value);
}

// Dates

const DATE_TIME_FORMAT = "MM/DD/YYYY hh:mm A";

/**
 * Formats an ISO timestamp (e.g. "07/14/2026 03:45 PM"), returning "—" when the
 * value is absent. Where a missing value should render nothing instead of a
 * dash, guard the call site rather than relying on this fallback.
 */
export function formatDateTime(value: string | null | undefined): string {
	return value ? dayjs(value).format(DATE_TIME_FORMAT) : "—";
}
