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
import java.util.HashSet;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;


@UserDefinedType(GpuV4Udt.UDT_NAME_GPUS_V4)
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Data
public class GpuV4Udt implements Serializable {

    public static final String UDT_NAME_GPUS_V4 = "gpusv4";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_CAPACITY = "capacity";
    public static final String COLUMN_INSTANCE_TYPES = "instance_types";

    @Column(COLUMN_NAME)
    String name;

    @Column(COLUMN_CAPACITY)
    int capacity;

    @Column(COLUMN_INSTANCE_TYPES)
    Set<InstanceTypeV3Udt> instanceTypes;

    public Set<InstanceTypeV3Udt> getInstanceTypes() {
        if (this.instanceTypes == null) {
            this.instanceTypes = new HashSet<>();
        }

        return this.instanceTypes;
    }
}
