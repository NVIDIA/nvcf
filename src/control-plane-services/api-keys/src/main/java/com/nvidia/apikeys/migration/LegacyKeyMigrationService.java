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

package com.nvidia.apikeys.migration;

import com.nvidia.apikeys.config.MigrationProperties;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.LegacyKeyScanDao;
import com.nvidia.apikeys.persistance.dao.OwnerStatusByAccountDao;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.LegacyAccountTranslator;
import com.nvidia.apikeys.services.LegacyKeyMaintenance;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeyVo;
import io.opentelemetry.api.GlobalOpenTelemetry;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.StatusCode;
import io.opentelemetry.api.trace.Tracer;
import java.nio.ByteBuffer;
import java.time.Duration;
import java.time.Instant;
import java.util.EnumMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.function.Supplier;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Copies every row of the legacy keys table into keys_v2 and
 * keys_by_owner_and_account_and_service for the configured legacy account.
 *
 * <p>A run holds the maintenance lease from {@link LegacyKeyMaintenance}, so key updates and
 * deletes for the legacy account are rejected while it copies. It waits for the grace period
 * before the first write, so requests that passed the maintenance check before the lease was
 * taken can finish. Only one replica runs at a time. The lease is renewed before every page.
 *
 * <p>A clean run records a completion marker before it releases the lease. Every later run on any
 * replica finds the marker and ends without taking the lease.
 *
 * <p>In the same window, it copies each owner's legacy status from keys_by_owner_and_service to
 * owner_status_by_account and owner_status_by_account_and_service, with the same value. This pass
 * has its own completion marker, so an environment that already copied its keys still copies
 * owner status. After that marker, the legacy account reads owner status from the v2 tables.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class LegacyKeyMigrationService {

    static final String SPAN_NAME = "apikeys.legacy-key-migration.run";
    static final AttributeKey<String> ORG_ID = AttributeKey.stringKey("nvcf.org.id");
    static final AttributeKey<Boolean> ERROR = AttributeKey.booleanKey("error");

    private final LegacyKeyScanDao scanDao;
    private final KeysV2Dao keysV2Dao;
    private final OwnerStatusByAccountDao ownerStatusDao;
    private final LegacyAccountTranslator legacyAccount;
    private final LegacyKeyMaintenance maintenance;
    private final MillisecondPrecisionClock clock;
    private final LegacyKeyMigrationMetrics metrics;
    private final NakProperties nakProperties;
    private final Tracer tracer = GlobalOpenTelemetry.getTracer("api-keys");

    public RunResult run() {
        var started = System.nanoTime();
        var counts = new EnumMap<KeyResult, Integer>(KeyResult.class);
        var ownerStatusCounts = new EnumMap<OwnerStatusResult, Integer>(OwnerStatusResult.class);
        var span = tracer.spanBuilder(SPAN_NAME)
                .setAttribute(ORG_ID, nakProperties.getNcaId())
                .startSpan();
        RunOutcome outcome;
        try (var ignored = span.makeCurrent()) {
            outcome = runWithLease(counts, ownerStatusCounts);
        } catch (RuntimeException e) {
            log.error("Legacy key migration run failed, ncaId={}", nakProperties.getNcaId(), e);
            span.recordException(e);
            outcome = RunOutcome.FAILED;
        }
        if (outcome == RunOutcome.FAILED || outcome == RunOutcome.LEASE_LOST) {
            span.setAttribute(ERROR, true);
            span.setStatus(StatusCode.ERROR, outcome.name());
        }
        span.end();

        var elapsed = Duration.ofNanos(System.nanoTime() - started);
        var result = new RunResult(outcome, counts, ownerStatusCounts);
        metrics.recordRun(outcome, elapsed);
        log.info("Legacy key migration run ended, ncaId={}, outcome={}, counts={}, "
                         + "ownerStatusCounts={}, elapsed={}",
                 nakProperties.getNcaId(), LegacyKeyMigrationMetrics.tagValue(outcome),
                 result.counts(), result.ownerStatusCounts(), elapsed);
        return result;
    }

    private RunOutcome runWithLease(
            Map<KeyResult, Integer> counts, Map<OwnerStatusResult, Integer> ownerStatusCounts) {
        var keysCompleted = maintenance.isCompleted();
        var ownerStatusCompleted = maintenance.isOwnerStatusCompleted();
        if (keysCompleted && ownerStatusCompleted) {
            log.info("Legacy key migration already completed, ncaId={}", nakProperties.getNcaId());
            return RunOutcome.ALREADY_COMPLETED;
        }
        var token = clock.instant();
        if (!maintenance.acquire(token)) {
            log.info("Legacy key migration lease held by another replica, ncaId={}",
                     nakProperties.getNcaId());
            return RunOutcome.LEASE_NOT_ACQUIRED;
        }
        log.info("Legacy key migration started, ncaId={}", nakProperties.getNcaId());
        try {
            sleep(settings().getMaintenanceGracePeriod());
            var outcome = RunOutcome.COMPLETED;
            if (!keysCompleted) {
                outcome = copyAll(token, counts);
                if (new RunResult(outcome, counts).isClean()) {
                    // Written while the lease is held, so no replica starts another pass first.
                    markCompleted();
                }
            }
            if (outcome == RunOutcome.COMPLETED && !ownerStatusCompleted) {
                outcome = copyOwnerStatuses(token, ownerStatusCounts);
                if (new RunResult(outcome, Map.of(), ownerStatusCounts).isClean()) {
                    markOwnerStatusCompleted();
                }
            }
            return outcome;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.info("Legacy key migration interrupted, ncaId={}", nakProperties.getNcaId());
            return RunOutcome.INTERRUPTED;
        } finally {
            release(token);
        }
    }

    private RunOutcome copyAll(Instant token, Map<KeyResult, Integer> counts)
            throws InterruptedException {
        ByteBuffer cursor = null;
        do {
            if (!maintenance.renew(token)) {
                log.warn("Legacy key migration lease lost, ncaId={}", nakProperties.getNcaId());
                return RunOutcome.LEASE_LOST;
            }
            var page = fetchPage(cursor);
            for (var row : page.rows()) {
                if (Thread.currentThread().isInterrupted()) {
                    throw new InterruptedException();
                }
                var result = migrate(row);
                counts.merge(result, 1, Integer::sum);
                metrics.recordKey(result);
            }
            cursor = page.next();
            if (cursor != null) {
                sleep(settings().getBatchDelay());
            }
        } while (cursor != null);
        return RunOutcome.COMPLETED;
    }

    private RunOutcome copyOwnerStatuses(
            Instant token, Map<OwnerStatusResult, Integer> counts) throws InterruptedException {
        var copied = new CopiedOwnerStatuses();
        ByteBuffer cursor = null;
        do {
            if (!maintenance.renew(token)) {
                log.warn("Legacy key migration lease lost, ncaId={}", nakProperties.getNcaId());
                return RunOutcome.LEASE_LOST;
            }
            var pagingState = cursor;
            var page = fetchPage(() -> scanDao.ownerPage(settings().getPageSize(), pagingState));
            for (var row : page.rows()) {
                if (Thread.currentThread().isInterrupted()) {
                    throw new InterruptedException();
                }
                var result = copyOwnerStatus(row, copied);
                counts.merge(result, 1, Integer::sum);
                metrics.recordOwnerStatus(result);
            }
            cursor = page.next();
            if (cursor != null) {
                sleep(settings().getBatchDelay());
            }
        } while (cursor != null);
        return RunOutcome.COMPLETED;
    }

    /**
     * Owners and owner and issuer service pairs already handled in this run. Each owner has many
     * rows, one per key, that all carry the same static owner status.
     */
    static final class CopiedOwnerStatuses {
        private final Set<List<Object>> accounts = new HashSet<>();
        private final Set<List<Object>> services = new HashSet<>();
    }

    /**
     * Copies the owner's legacy status, with the same value, to the account row and to the row
     * for the row's issuer service. A row is written only when its stored status differs, so a
     * pass that writes nothing proves the copy is complete.
     */
    OwnerStatusResult copyOwnerStatus(KeyByOwnerAndServiceModel row, CopiedOwnerStatuses copied) {
        var ncaId = nakProperties.getNcaId();
        var ownerType = row.getOwnerType();
        var ownerId = row.getOwnerId();
        var status = row.getOwnerStatus();
        var firstRowOfOwner = copied.accounts.add(List.of(ownerType, ownerId));
        if (status == null) {
            if (firstRowOfOwner) {
                log.warn("Legacy owner status skipped, status is null, ncaId={}, ownerType={}, "
                                 + "ownerId={}", ncaId, ownerType, ownerId);
            }
            return OwnerStatusResult.SKIPPED;
        }
        try {
            var at = Optional.ofNullable(row.getOwnerStatusUpdatedAt()).orElseGet(clock::instant);
            var wrote = false;
            if (firstRowOfOwner && !status.equals(ownerStatusDao
                    .getOwnerStatusByAccount(ncaId, ownerType, ownerId)
                    .map(OwnerStatusByAccountModel::getOwnerStatus)
                    .orElse(null))) {
                ownerStatusDao.saveOwnerStatusByAccount(ncaId, ownerType, ownerId, status, at);
                wrote = true;
            }
            var serviceId = Optional.ofNullable(row.getIssuerServiceId())
                    .flatMap(legacyAccount::findServiceId);
            if (serviceId.isPresent()
                    && copied.services.add(List.of(ownerType, ownerId, serviceId.get()))
                    && !status.equals(ownerStatusDao
                    .getOwnerStatusByAccountAndService(ncaId, ownerType, ownerId, serviceId.get())
                    .map(OwnerStatusByAccountAndServiceModel::getOwnerStatus)
                    .orElse(null))) {
                ownerStatusDao.saveOwnerStatusByAccountAndService(
                        ncaId, ownerType, ownerId, serviceId.get(), status, at);
                wrote = true;
            }
            return wrote ? OwnerStatusResult.COPIED : OwnerStatusResult.ALREADY_COPIED;
        } catch (RuntimeException e) {
            log.error("Legacy key migration failed to copy an owner status, ncaId={}, "
                              + "ownerType={}, ownerId={}, issuerServiceId={}",
                      ncaId, ownerType, ownerId, row.getIssuerServiceId(), e);
            return OwnerStatusResult.FAILED;
        }
    }

    private LegacyKeyScanDao.Page fetchPage(ByteBuffer cursor) throws InterruptedException {
        return fetchPage(() -> scanDao.page(settings().getPageSize(), cursor));
    }

    private <T> T fetchPage(Supplier<T> fetch) throws InterruptedException {
        var attempts = settings().getPageFetchAttempts();
        for (var attempt = 1; ; attempt++) {
            try {
                return fetch.get();
            } catch (RuntimeException e) {
                if (attempt >= attempts) {
                    throw e;
                }
                log.warn("Legacy key migration page fetch failed, ncaId={}, attempt={}/{}",
                         nakProperties.getNcaId(), attempt, attempts, e);
                sleep(settings().getPageFetchBackoff());
            }
        }
    }

    KeyResult migrate(KeyModel row) {
        KeyVo legacy = null;
        try {
            legacy = scanDao.decrypt(row);
            if (legacyAccount.findServiceId(legacy.getIssuerServiceId()).isEmpty()) {
                log.warn("Legacy key skipped, issuer service is not in apikeys.service-id-map, "
                                 + "ncaId={}, keyId={}, ownerType={}, ownerId={}, issuerServiceId={}",
                         nakProperties.getNcaId(), legacy.getKeyId(), legacy.getOwnerType(),
                         legacy.getOwnerId(), legacy.getIssuerServiceId());
                return KeyResult.SKIPPED;
            }
            var key = legacyAccount.fromLegacy(legacy);
            if (isMigrated(key)) {
                return KeyResult.ALREADY_MIGRATED;
            }
            keysV2Dao.save(key);
            return KeyResult.MIGRATED;
        } catch (RuntimeException e) {
            if (legacy == null) {
                log.error("Legacy key migration failed to decrypt a key, ncaId={}",
                          nakProperties.getNcaId(), e);
            } else {
                log.error("Legacy key migration failed to copy a key, ncaId={}, keyId={}, "
                                  + "ownerType={}, ownerId={}, issuerServiceId={}",
                          nakProperties.getNcaId(), legacy.getKeyId(), legacy.getOwnerType(),
                          legacy.getOwnerId(), legacy.getIssuerServiceId(), e);
            }
            return KeyResult.FAILED;
        }
    }

    private boolean isMigrated(KeyV2Vo key) {
        return keysV2Dao.getKeyByHash(key.getKeyHash()).isPresent()
                && keysV2Dao.get(key.getNcaId(), key.getOwnerType(), key.getOwnerId(),
                                 key.getIssuerServiceId(), key.getKeyId()).isPresent();
    }

    private void markCompleted() {
        try {
            maintenance.markCompleted(clock.instant());
            log.info("Legacy key migration recorded completion, ncaId={}", nakProperties.getNcaId());
        } catch (RuntimeException e) {
            log.warn("Legacy key migration failed to record completion, other replicas run again, "
                             + "ncaId={}", nakProperties.getNcaId(), e);
        }
    }

    private void markOwnerStatusCompleted() {
        try {
            maintenance.markOwnerStatusCompleted(clock.instant());
            log.info("Legacy key migration recorded owner status completion, ncaId={}",
                     nakProperties.getNcaId());
        } catch (RuntimeException e) {
            log.warn("Legacy key migration failed to record owner status completion, other "
                             + "replicas run again, ncaId={}", nakProperties.getNcaId(), e);
        }
    }

    private void release(Instant token) {
        try {
            if (!maintenance.release(token)) {
                log.warn("Legacy key migration lease was not held at release, ncaId={}",
                         nakProperties.getNcaId());
            }
        } catch (RuntimeException e) {
            log.warn("Legacy key migration failed to release the lease, it expires with its TTL, "
                             + "ncaId={}", nakProperties.getNcaId(), e);
        }
    }

    private MigrationProperties.LegacyKeys settings() {
        return nakProperties.getMigration().getLegacyKeys();
    }

    private static void sleep(Duration duration) throws InterruptedException {
        if (duration.isPositive()) {
            Thread.sleep(duration);
        }
    }
}
