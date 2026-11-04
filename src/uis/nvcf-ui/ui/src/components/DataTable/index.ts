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

import { ActiveFilters } from "./ActiveFilters";
import { ColumnVisibility } from "./ColumnVisibility";
import { Content } from "./Content";
import { DataTable as DataTableRoot } from "./DataTable";
import { Filters } from "./Filters";
import { ItemCount } from "./ItemCount";
import { Pagination } from "./Pagination";
import { Search } from "./Search";
import {
	DataTableSkeleton,
	SkeletonContent,
	SkeletonToolbar,
} from "./Skeleton";
import { Sort } from "./Sort";
import { Toolbar } from "./Toolbar";

const DataTable = Object.assign(DataTableRoot, {
	Toolbar,
	Content,
	Pagination,
	Search,
	ItemCount,
	Sort,
	Filters,
	ActiveFilters,
	ColumnVisibility,
	Skeleton: DataTableSkeleton,
	SkeletonToolbar,
	SkeletonContent,
});

export { DataTable };
