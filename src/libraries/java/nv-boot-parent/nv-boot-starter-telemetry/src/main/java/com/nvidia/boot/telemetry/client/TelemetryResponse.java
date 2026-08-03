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

import java.util.Optional;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

/**
 * Generic response from a Telemetry server after sending CloudEvents.
 * Use {@code TelemetryResponse<Map<String, Object>>} for JSON responses, or
 * {@code TelemetryResponse<Void>} for empty responses (e.g., 204 No Content).
 *
 * @param <T> the response body type (e.g., Map for JSON, Void for empty)
 */
@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
public class TelemetryResponse<T> {

    /**
     * HTTP status code from the Telemetry server.
     */
    private int statusCode;

    /**
     * Response body, or null for empty responses (e.g., 204 No Content).
     */
    private T body;

    /**
     * Returns the response body as an Optional.
     */
    public Optional<T> getBodyOptional() {
        return Optional.ofNullable(body);
    }
}
