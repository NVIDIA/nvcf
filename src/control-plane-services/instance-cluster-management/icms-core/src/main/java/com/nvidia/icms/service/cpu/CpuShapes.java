/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.icms.service.cpu;

import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.apache.commons.lang3.StringUtils;

/**
 * Pure predicates that define what a CPU-only instance type is.
 *
 * <p>CPU-only workloads use the reserved GPU name {@value #CPU_GPU_NAME} and instance types named
 * {@code <provider>.CPU.<shape>} with no GPU. Nothing here depends on configuration or on the
 * feature switch, so the same definition is used by registration checks, destination selection,
 * capacity and usage reporting.
 */
public final class CpuShapes {

    /** Reserved GPU name for CPU-only workloads. */
    public static final String CPU_GPU_NAME = "CPU";

    private static final String NAME_SEPARATOR = "\\.";
    private static final int NAME_PARTS = 3;
    private static final int SHAPE_MARKER_INDEX = 1;
    private static final int MAX_LABEL_VALUE_LENGTH = 63;
    // The name is the value of the node label NVCA selects the CPU node group by, so it must be a valid
    // Kubernetes label value.
    private static final Pattern LABEL_VALUE = Pattern.compile("[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?");
    // A Kubernetes resource quantity: a number with an optional binary, decimal or exponent suffix.
    private static final Pattern QUANTITY = Pattern.compile(
            "\\+?(\\d+(?:\\.\\d*)?|\\.\\d+)(?:Ki|Mi|Gi|Ti|Pi|Ei|n|u|m|k|M|G|T|P|E|[eE][+-]?\\d+)?");

    private CpuShapes() {
    }

    /** True when the GPU name is the reserved name, in any letter case. */
    public static boolean isReservedGpuName(String gpuName) {
        return CPU_GPU_NAME.equalsIgnoreCase(gpuName);
    }

    /** True when the GPU name is exactly the canonical reserved name. */
    public static boolean isCanonicalGpuName(String gpuName) {
        return CPU_GPU_NAME.equals(gpuName);
    }

    /**
     * True when the instance type name has exactly three dot-separated non-blank parts and the
     * middle part is {@code CPU}, for example {@code NCP.CPU.4c16g}.
     */
    public static boolean hasShapeName(String instanceTypeName) {
        if (StringUtils.isBlank(instanceTypeName)) {
            return false;
        }
        String[] parts = instanceTypeName.split(NAME_SEPARATOR, -1);
        if (parts.length != NAME_PARTS) {
            return false;
        }
        for (String part : parts) {
            if (StringUtils.isBlank(part)) {
                return false;
            }
        }
        return CPU_GPU_NAME.equalsIgnoreCase(parts[SHAPE_MARKER_INDEX]);
    }

    /**
     * A CPU shape entry: the reserved GPU name, a CPU shape name and no GPU. This is the entry
     * ICMS must never process as a GPU instance type.
     */
    public static boolean isCpuShapeEntry(String gpuName, String instanceTypeName, int gpuCount) {
        return isReservedGpuName(gpuName) && hasShapeName(instanceTypeName) && gpuCount == 0;
    }

    /**
     * True when the value is a positive Kubernetes resource quantity such as {@code 16Gi} or
     * {@code 500m}, the form NVCA parses for the shape's memory and storage.
     */
    public static boolean isPositiveQuantity(String quantity) {
        if (StringUtils.isBlank(quantity)) {
            return false;
        }
        Matcher m = QUANTITY.matcher(quantity);
        return m.matches() && m.group(1).chars().anyMatch(c -> c >= '1' && c <= '9');
    }

    private static boolean isLabelValue(String value) {
        return value.length() <= MAX_LABEL_VALUE_LENGTH && LABEL_VALUE.matcher(value).matches();
    }

    /**
     * A valid CPU shape for a registration that does not carry storage: the name and the value are
     * the same shape name and a valid label value, there is no GPU, the cores are positive and the
     * memory is a positive quantity.
     */
    public static boolean isValidShape(
            String name, String value, int gpuCount, int cpuCores, String systemMemory) {
        return hasShapeName(name)
                && name.equals(value)
                && isLabelValue(name)
                && gpuCount == 0
                && cpuCores > 0
                && isPositiveQuantity(systemMemory);
    }

    /** A valid CPU shape that also declares a positive storage quantity, as NVCA registers it. */
    public static boolean isValidShape(
            String name, String value, int gpuCount, int cpuCores, String systemMemory, String storage) {
        return isValidShape(name, value, gpuCount, cpuCores, systemMemory)
                && isPositiveQuantity(storage);
    }
}
