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

package com.nvidia.apikeys.services;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Reports where the legacy key migration is, from the rows in row_update_lock. Also reports
 * whether legacy owner status has been copied, from its own completion marker.
 *
 * <p>{@link Phase#AFTER} is cached for the life of the process once the completion marker is
 * seen, so later calls never read Cassandra. Deleting the marker to run the migration again
 * takes effect only after a restart. The other phases are read again after
 * {@link #REFRESH_INTERVAL}. A failed read keeps the last known phase, or {@link Phase#BEFORE}
 * if there is none, because BEFORE reads both tables and is correct in every phase.
 */
@Slf4j
@Service
public class LegacyMigrationState {

    public enum Phase {
        BEFORE,
        DURING,
        AFTER
    }

    static final Duration REFRESH_INTERVAL = Duration.ofSeconds(30);

    private final LegacyKeyMaintenance maintenance;
    private final Clock clock;

    private volatile Phase phase;
    private volatile Instant refreshAt = Instant.MIN;
    private volatile boolean ownerStatusMigrated;
    private volatile Instant ownerStatusRefreshAt = Instant.MIN;

    public LegacyMigrationState(LegacyKeyMaintenance maintenance, Clock clock) {
        this.maintenance = maintenance;
        this.clock = clock;
    }

    public Phase current() {
        Phase known = phase;
        if (known == Phase.AFTER) {
            return known;
        }
        Instant now = clock.instant();
        if (known != null && now.isBefore(refreshAt)) {
            return known;
        }
        Phase read = read(known);
        if (read != known) {
            log.info("Legacy key migration phase is {}", read);
        }
        phase = read;
        refreshAt = now.plus(REFRESH_INTERVAL);
        return read;
    }

    public boolean isCompleted() {
        return current() == Phase.AFTER;
    }

    /**
     * True once the owner status completion marker is seen. Cached and refreshed like
     * {@link Phase#AFTER}. A failed read keeps the last known value, or false if there is none,
     * so the legacy account keeps reading the legacy owner status column.
     */
    public boolean isOwnerStatusMigrated() {
        if (ownerStatusMigrated) {
            return true;
        }
        Instant now = clock.instant();
        if (now.isBefore(ownerStatusRefreshAt)) {
            return false;
        }
        boolean read;
        try {
            read = maintenance.isOwnerStatusCompleted();
        } catch (RuntimeException e) {
            log.warn("Failed to read legacy owner status migration marker, using false", e);
            read = false;
        }
        if (read) {
            log.info("Legacy owner status migration is completed");
        }
        ownerStatusMigrated = read;
        ownerStatusRefreshAt = now.plus(REFRESH_INTERVAL);
        return read;
    }

    /**
     * Drops the cached state, so the next call reads row_update_lock again.
     */
    public void forget() {
        phase = null;
        refreshAt = Instant.MIN;
        ownerStatusMigrated = false;
        ownerStatusRefreshAt = Instant.MIN;
    }

    private Phase read(Phase known) {
        try {
            if (maintenance.isCompleted()) {
                return Phase.AFTER;
            }
            return maintenance.isRunning() ? Phase.DURING : Phase.BEFORE;
        } catch (RuntimeException e) {
            Phase fallback = known == null ? Phase.BEFORE : known;
            log.warn("Failed to read legacy key migration phase, using {}", fallback, e);
            return fallback;
        }
    }
}
