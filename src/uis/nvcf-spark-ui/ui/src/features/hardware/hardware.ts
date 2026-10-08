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

/** The systems a recipe can be built for, in the order the UI lists them. */
export const HARDWARE_CLASSES = ["DGX Spark", "DGX Station"] as const;

export type HardwareClass = (typeof HARDWARE_CLASSES)[number];

/**
 * The system a GPU product belongs to, from its GPU Feature Discovery name
 * (`nvidia.com/gpu.product`): GB10 is DGX Spark's, GB300 is DGX Station's.
 * Spellings vary (`NVIDIA-GB10`, `NVIDIA GB10`, `NVIDIA_GB10`), so only the
 * chip's name is compared. Unknown products have no class.
 */
export function hardwareClass(gpuProduct: string): HardwareClass | undefined {
	const chips = gpuProduct.toUpperCase().split(/[^A-Z0-9]+/);
	if (chips.includes("GB10")) return "DGX Spark";
	if (chips.includes("GB300")) return "DGX Station";
	return undefined;
}

/** `value` if it names a hardware class, for parsing a URL's `?hardware=`. */
export function asHardwareClass(value: unknown): HardwareClass | undefined {
	return HARDWARE_CLASSES.find((hardware) => hardware === value);
}
