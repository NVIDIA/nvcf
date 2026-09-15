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

package com.nvidia.boot.telemetry.client;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.Map;
import org.junit.jupiter.api.Test;

class TelemetryResponseTest {

    @Test
    void builderAndGetBodyOptional() {
        Map<String, Object> body = Map.of("status", (Object) "ok");
        var response = TelemetryResponse.<Map<String, Object>>builder()
                .statusCode(200)
                .body(body)
                .build();

        assertThat(response.getStatusCode()).isEqualTo(200);
        assertThat(response.getBodyOptional()).contains(body);
    }

    @Test
    void emptyBodyOptionalWhenBodyNull() {
        var response = TelemetryResponse.<Void>builder()
                .statusCode(204)
                .body(null)
                .build();

        assertThat(response.getBodyOptional()).isEmpty();
    }
}
