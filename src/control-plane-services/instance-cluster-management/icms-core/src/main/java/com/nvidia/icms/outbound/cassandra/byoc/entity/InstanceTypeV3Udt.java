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
package com.nvidia.icms.outbound.cassandra.byoc.entity;

import java.io.Serializable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@UserDefinedType(InstanceTypeV3Udt.UDT_NAME_INSTANCE_TYPE_V3)
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Data

/*
 InstanceTypeV3Udt will be used in GpuV4Udt
 */
public class InstanceTypeV3Udt implements Serializable {

    public static final String UDT_NAME_INSTANCE_TYPE_V3 = "instancetypev3";
    public static final String COLUMN_CPU_CORES = "cpu_cores";
    public static final String COLUMN_SYSTEM_MEMORY = "system_memory";
    public static final String COLUMN_GPU_MEMORY = "gpu_memory";
    public static final String COLUMN_GPU_COUNT = "gpu_count";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_DESCRIPTION = "description";
    public static final String COLUMN_VALUE = "value";
    public static final String COLUMN_IS_DEFAULT = "is_default";
    public static final String COLUMN_CPU_ARCH = "cpu_arch";
    public static final String COLUMN_OS = "os";
    public static final String COLUMN_DRIVER_VERSION = "driver_version";
    public static final String COLUMN_STORAGE = "storage";


    @Column(COLUMN_CPU_CORES)
    int cpuCores;

    @Column(COLUMN_SYSTEM_MEMORY)
    String systemMemory;

    @Column(COLUMN_GPU_MEMORY)
    String gpuMemory;

    @Column(COLUMN_GPU_COUNT)
    int gpuCount = 1;

    @Column(COLUMN_NAME)
    String name;

    @Column(COLUMN_DESCRIPTION)
    String description;

    @Column(COLUMN_IS_DEFAULT)
    Boolean isDefault;

    @Column(COLUMN_VALUE)
    String value;

    @Column(COLUMN_CPU_ARCH)
    String cpuArch;

    @Column(COLUMN_OS)
    String os;

    @Column(COLUMN_DRIVER_VERSION)
    String driverVersion;

    @Column(COLUMN_STORAGE)
    String storage;
}
