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
package com.nvidia.nvct.persistence.task.entity;

import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@NoArgsConstructor
@Builder(toBuilder = true)
@UserDefinedType(TelemetriesUdt.USER_DEFINED_TYPE_NAME)
public class TelemetriesUdt {
    public static final String USER_DEFINED_TYPE_NAME = "telemetries_udt";

    @Column("logs_telemetry_id")
    private UUID logsTelemetryId;

    @Column("metrics_telemetry_id")
    private UUID metricsTelemetryId;

    @Column("traces_telemetry_id")
    private UUID tracesTelemetryId;
}
