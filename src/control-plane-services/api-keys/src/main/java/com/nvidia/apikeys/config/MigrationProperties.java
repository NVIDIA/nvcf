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

package com.nvidia.apikeys.config;

import jakarta.validation.Valid;
import jakarta.validation.constraints.AssertTrue;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import java.time.Duration;
import lombok.Data;
import org.springframework.boot.context.properties.NestedConfigurationProperty;

/**
 * Settings under apikeys.migration.
 */
@Data
public class MigrationProperties {

    @Valid
    @NotNull
    @NestedConfigurationProperty
    private LegacyKeys legacyKeys = new LegacyKeys();

    /**
     * Settings for copying legacy keys into keys_v2. The task runs only when enabled is true.
     * The first deploy that ships the task must keep it disabled.
     */
    @Data
    public static class LegacyKeys {

        private boolean enabled = false;

        @NotNull
        private Duration initialDelay = Duration.ofMinutes(1);

        @NotNull
        private Duration fixedDelay = Duration.ofMinutes(10);

        @Positive
        private int pageSize = 100;

        @NotNull
        private Duration batchDelay = Duration.ofMillis(200);

        @Positive
        private int pageFetchAttempts = 3;

        @NotNull
        private Duration pageFetchBackoff = Duration.ofSeconds(1);

        // Must be longer than one batch. The lease is renewed before every batch.
        @NotNull
        private Duration leaseDuration = Duration.ofMinutes(2);

        // Must be longer than the Cassandra request timeout so in-flight writes finish. The
        // lease is not renewed during this wait.
        @NotNull
        private Duration maintenanceGracePeriod = Duration.ofSeconds(15);

        @AssertTrue(message = "maintenance-grace-period must be shorter than lease-duration")
        public boolean isMaintenanceGracePeriodShorterThanLease() {
            return maintenanceGracePeriod == null || leaseDuration == null
                    || maintenanceGracePeriod.compareTo(leaseDuration) < 0;
        }
    }
}
