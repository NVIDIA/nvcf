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

@UserDefinedType(GpuUdt.UDT_NAME_GPU)
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Data

public class GpuUdt implements Serializable {

    public static final String UDT_NAME_GPU = "gpu";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_INSTANCE_TYPES = "instance_types";

    @Column(COLUMN_NAME)
    String name;

    @Column(COLUMN_INSTANCE_TYPES)
    Set<InstanceTypeUdt> instanceTypes;

    public Set<InstanceTypeUdt> getInstanceTypes() {
        if (this.instanceTypes == null) {
            this.instanceTypes = new HashSet<>();
        }

        return this.instanceTypes;
    }

}
