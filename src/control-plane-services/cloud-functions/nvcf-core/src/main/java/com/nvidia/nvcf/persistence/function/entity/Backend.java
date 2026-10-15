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
package com.nvidia.nvcf.persistence.function.entity;

import static com.nvidia.nvcf.persistence.function.entity.Gpu.A100;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.A100_80GB;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.A100_80GB_8GPU;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.A10G;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.L40;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.L40G;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.T10;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.T4;
import static com.nvidia.nvcf.persistence.function.entity.Gpu.V100;
import static java.lang.String.format;

import com.google.common.collect.BiMap;
import com.google.common.collect.ImmutableBiMap;
import java.util.EnumSet;
import java.util.Optional;
import lombok.AccessLevel;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

// The strings are from the table in the SDD. We can move them to configuration later,
// if needed.
@Slf4j
public enum Backend {

    AWS("AWS", ImmutableBiMap.of(A10G, "g5.2xlarge",
                                 T4, "g4dn.2xlarge",
                                 V100, "p3.2xlarge")),
    AZURE("AZURE", ImmutableBiMap.of()),
    GCP("GCP", ImmutableBiMap.of(A100, "a2-highgpu-1g",
                                 T4, "nvidia-tesla-t4",
                                 V100, "nvidia-tesla-v100")),
    GFN("GFN", ImmutableBiMap.of(A10G, "ga10g_1.br20_2xlarge",
                                 L40G, "gl40g_1.br25_2xlarge",
                                 T10, "g6.full", // gt10_1.br10_large
                                 L40, "gl40_1.br20_2xlarge")),
    OCI("OCI", ImmutableBiMap.of(V100, "VM.GPU3.1",
                                 A100_80GB, "BM.GPU.A100-v2.8",
                                 A100_80GB_8GPU, "BM.GPU.A100-v2.8_8x")),
    UNDEFINED("UNDEFINED", ImmutableBiMap.of());

    private static final String MESG_GPU_UNAVAILABLE_IN_BACKEND =
            "GPU '{}' is not available in '{}' backend";

    private final String name;
    @Getter(AccessLevel.PACKAGE)
    private final BiMap<Gpu, String> gpuToInstanceTypeMap;

    Backend(String name, BiMap<Gpu, String> gpuTypeToInstanceTypeMap) {
        this.name = name;
        this.gpuToInstanceTypeMap = gpuTypeToInstanceTypeMap;
    }

    public Optional<String> getInstanceType(Gpu gpu) {
        var instanceType = gpuToInstanceTypeMap.get(gpu);
        if (StringUtils.isBlank(instanceType)) {
            log.info(MESG_GPU_UNAVAILABLE_IN_BACKEND, gpu.toString(), this.name);
        }

        return Optional.ofNullable(instanceType);
    }

    public Optional<Gpu> getGpu(String instanceType) {
        return Optional.ofNullable(gpuToInstanceTypeMap.inverse().get(instanceType));
    }

    @Override
    public String toString() {
        return this.name;
    }

    public static Backend fromText(String val) {
        return EnumSet.allOf(Backend.class)
                .stream()
                .filter(e -> e.name.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalStateException(format("Unsupported enum %s.", val)));
    }

}
