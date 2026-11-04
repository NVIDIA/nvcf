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
package com.nvidia.nvcf.service.telemetry;

import com.nvidia.boot.exceptions.BadRequestException;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class TelemetryAvailability {

    private static final String MESG_TELEMETRY_UNSUPPORTED =
            "telemetry is not supported on this deployment";
    private static final String MESG_TELEMETRY_DISABLED =
            "Telemetry is disabled on this deployment: creating a telemetry, or linking one to a "
                    + "function, returns 400";

    private final boolean enabled;

    public TelemetryAvailability(@Value("${nvcf.telemetry.enabled:true}") boolean enabled) {
        this.enabled = enabled;
        if (!enabled) {
            log.info(MESG_TELEMETRY_DISABLED);
        }
    }

    public void requireEnabled() {
        if (!enabled) {
            throw new BadRequestException(MESG_TELEMETRY_UNSUPPORTED);
        }
    }
}
