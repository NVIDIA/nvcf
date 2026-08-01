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
package com.nvidia.icms.service.extensions.impl;

import com.nvidia.icms.service.extensions.api.ReservedBackupInstanceProcessor;

import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import lombok.extern.slf4j.Slf4j;

/**
 * Default {@link ReservedBackupInstanceProcessor} used when the real non-BYOC
 * implementation is absent from the classpath (future ICMS-core-only deployment).
 *
 * <p>{@code isBackupToPrimaryZoneMigrationEnabled()} returns {@code false}, which
 * causes {@code ActiveInstanceMonitoringTaskService} to never invoke {@code execute}
 * and — combined with {@code isCloudFailureDetectionEnabled() == false} — to
 * short-circuit the entire monitoring run early. {@code execute} is therefore
 * unreachable through normal flow but a safe no-op if invoked.
 */
@Slf4j
public class NoOpReservedBackupInstanceProcessor implements ReservedBackupInstanceProcessor {

    @Override
    public boolean isBackupToPrimaryZoneMigrationEnabled() {
        return false;
    }

    @Override
    public void execute(@NotNull List<InstanceV2Entity> activeReservedBackupInstances) {
        log.debug("NoOpReservedBackupInstanceProcessor.execute called with {} instances — no-op",
                  activeReservedBackupInstances.size());
    }
}
