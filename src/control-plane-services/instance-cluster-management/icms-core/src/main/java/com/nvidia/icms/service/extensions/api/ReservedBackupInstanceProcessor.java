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
package com.nvidia.icms.service.extensions.api;

import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.extensions.impl.NoOpReservedBackupInstanceProcessor;
import jakarta.validation.constraints.NotNull;
import java.util.List;

/**
 * Extension that allows the optional backend-specific "reserved backup instance" lifecycle
 * work driven by the {@code ActiveInstanceMonitoringTaskService} core scheduler.
 *
 * <p>Two responsibilities are bundled here so that the core scheduler does not have
 * to inject {@code IcmsConfigurationProperties} or the concrete processor directly:
 *
 * <ul>
 *   <li>{@link #isBackupToPrimaryZoneMigrationEnabled()} — feature-flag gate read by the
 *       core scheduler to decide whether to short-circuit the whole monitoring run and
 *       whether to invoke {@link #execute(List)}.</li>
 *   <li>{@link #execute(List)} — actual processing of healthy {@code RESERVED_BACKUP}
 *       instances (immediate-termination + incremental migration scheduling). Real
 *       implementation lives in the internal backend module.</li>
 * </ul>
 *
 * <p>For self-hosted deployment, {@link NoOpReservedBackupInstanceProcessor} returns
 * {@code false} from the flag check and is a no-op on {@code execute}, so the scheduler
 * simply never enters the backup-migration branch.
 */
public interface ReservedBackupInstanceProcessor {

    /**
     * Returns {@code true} when the non-BYOC backup-to-primary zone migration feature is
     * enabled. The core scheduler uses this both as a top-level skip gate and as the
     * per-cycle invocation gate.
     */
    boolean isBackupToPrimaryZoneMigrationEnabled();

    /**
     * Processes the supplied list of healthy {@code RESERVED_BACKUP} instances:
     * terminates expired / orphaned ones immediately and schedules incremental
     * back-to-primary migration for the rest.
     */
    void execute(@NotNull List<InstanceV2Entity> activeReservedBackupInstances);
}
