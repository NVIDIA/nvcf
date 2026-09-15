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

public enum TelemetryProtocolEnum {
    HTTP("HTTP"),
    GRPC("GRPC");

    private static final String MESG_UNSUPPORTED_TELEMETRY_PROTOCOL =
            "Unsupported telemetry protocol: '%s'";

    private final String name;

    TelemetryProtocolEnum(String name) {
        this.name = name;
    }

    @Override
    public String toString() {
        return this.name;
    }

    public static TelemetryProtocolEnum fromText(@NonNull String val) {
        return EnumSet.allOf(TelemetryProtocolEnum.class)
                .stream()
                .filter(e -> e.name.equalsIgnoreCase(val))
                .findFirst()
                .orElseThrow(() -> new IllegalArgumentException(String.format(MESG_UNSUPPORTED_TELEMETRY_PROTOCOL, val)));
    }

}
