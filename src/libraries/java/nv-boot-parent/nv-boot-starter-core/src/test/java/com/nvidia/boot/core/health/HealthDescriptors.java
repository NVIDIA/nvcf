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

package com.nvidia.boot.core.health;

import org.springframework.boot.health.actuate.endpoint.HealthDescriptor;
import org.springframework.boot.health.actuate.endpoint.IndicatedHealthDescriptor;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.Status;

final class HealthDescriptors {

    private HealthDescriptors() {}

    static HealthDescriptor withStatus(Status status) {
        try {
            var constructor = IndicatedHealthDescriptor.class.getDeclaredConstructor(Health.class);
            constructor.setAccessible(true);
            return constructor.newInstance(Health.status(status).build());
        } catch (ReflectiveOperationException ex) {
            throw new IllegalStateException("Failed to create health descriptor for tests", ex);
        }
    }
}
