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
package com.nvidia.nvct.service.telemetry.dto;

import java.util.EnumSet;
import lombok.NonNull;

public enum TelemetryProviderEnum {
    PROMETHEUS("PROMETHEUS"),
    GRAFANA_CLOUD("GRAFANA_CLOUD"),
    SPLUNK("SPLUNK"),
    DATADOG("DATADOG"),
    SERVICENOW("SERVICENOW"),
    KRATOS("KRATOS"),
    KRATOS_THANOS("KRATOS_THANOS"),
    TIMESTREAM("TIMESTREAM"),
    VICTORIAMETRICS("VICTORIAMETRICS"),
    AZURE_MONITOR("AZURE_MONITOR"),
    OTEL_COLLECTOR("OTEL_COLLECTOR");

    private static final String MESG_UNSUPPORTED_TELEMETRY_PROVIDER =
            "Unsupported telemetry provider: '%s'";

    private final String name;

    TelemetryProviderEnum(String name) {
        this.name = name;
    }

    @Override
    public String toString() {
        return this.name;
    }

    public static TelemetryProviderEnum fromText(@NonNull String val) {
        return EnumSet.allOf(TelemetryProviderEnum.class)
                .stream()
                .filter(e -> e.name.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalArgumentException(String.format(MESG_UNSUPPORTED_TELEMETRY_PROVIDER,val)));
    }
}
