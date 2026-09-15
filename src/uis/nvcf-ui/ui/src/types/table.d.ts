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

import "@tanstack/react-table";

declare module "@tanstack/react-table" {
	interface ColumnMeta<TData extends RowData, TValue> {
		filterVariant?: "single-select" | "multi-select";
		filterOptions?: { label: string; value: string }[];
		sortType?: "text" | "date" | "number";
		sortLabels?: { asc: string; desc: string };
	}

	interface FilterFns {
		includesValue: FilterFn<RowData>;
		multiSelect: FilterFn<RowData>;
	}
}
