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
package com.nvidia.icms.service.scheduled.cleanup;

import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;

import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.SliceResult;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.PeriodOfTime;
import com.nvidia.icms.util.TimeUtils;
import java.nio.ByteBuffer;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@AllArgsConstructor
@Service
public class DatabaseCleanupTask<T> {

    public static final String STARTED_ALL_DAYS = "STARTED_ALL_DAYS";
    public static final String STARTED = "STARTED";
    public static final String SKIPPED_ACTIVE = "SKIPPED_ACTIVE";
    public static final String DELETED_DAY_DATA = "DELETED_DAY_DATA";
    public static final String DELETED_ALL_RECORDS = "DELETED_ALL_RECORDS";
    public static final String SKIPPED_NO_DATA = "SKIPPED_NO_DATA";
    public static final String COMPLETED_ALL_DAYS = "COMPLETED_ALL_DAYS";

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final TelemetryEventClient telemetryEventClient;

    private final LockProviderService lockProviderService;

    public void execute(DatabaseCleanupExecutor<T> cleanupExecutor, String jobName) {

        /*
        1. Check if feature is enabled if not then return
        2. Generate a list of days for last X months
        3. Randomize this list
        4. Go over all days in the list
        5. Try to get lock "cleanup"-"table_name"-"date"
        6. If lock not obtained, try to get another day
        7. read a single record from this day
        8. if day does not exist, go to next day
        9. check if day is beyond limit (like 90 days - conf.)
        10. If so, the entire day has to be deleted
            a. Load all records for this day
            b. check if there is any active records
            c. Do clean up by deleting related records from other tables (like "requests")
            d. after all records in the day processed, delete the entire day (by primary key)
            e. we might use TTL to simplify logic, but we can not change TTL after record is inserted
        11. If day is inside of limit (90 days)
            a. Read all records for this day
            b. Check if any record  for this dat is not deleted.
            c. as soon as first active records found, skip this day
            d. if all records ara marked as deleted, delete the entire day (PK)
         */

        if (cleanupExecutor == null) {
            log.info(" DatabaseCleanupExecutor is not provided, avoiding further processing");
            return;
        }

        // 1. Check if feature is enabled if not then return
        if (!icmsConfigurationProperties.isDatabaseCleanupTaskEnabled()) {
            log.info("DatabaseCleanupTask is not enabled, avoiding further processing");
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        stopwatch.start();

        try {
            sendTelemetryEvent(STARTED_ALL_DAYS, jobName, null, null);

            log.info("Started {} task. Generating range of days for processing", jobName);

            int daysToCheck = icmsConfigurationProperties.getDatabaseCleanupLookupPeriodInDays();
            int daysToKeep = icmsConfigurationProperties.getDatabaseRecordsTtlInDays();

            log.info("Database cleanup task {}: daysToCheck {}, daysToKeep {}", jobName, daysToCheck, daysToKeep);

            Instant today = Instant.now().truncatedTo(ChronoUnit.DAYS);
            List<PeriodOfTime> periodsToCheck = PeriodOfTime.buildPeriodsForDates(
                    TimeUtils.getPreviousDate(daysToCheck),
                    today,
                    1
            );

            Collections.shuffle(periodsToCheck);

            periodsToCheck.forEach(period -> {
                String lockName = String.format("databaseCleanup-%s-%s", jobName, period.start().toString());
                if (lockProviderService.obtainLockWithTtl(lockName,
                                                           icmsConfigurationProperties.getDatabaseCleanupLockTtlInSeconds())) {
                    executeCleanupLogic(cleanupExecutor, jobName, period.start(), daysToKeep);
                }
            });

            log.info("Completed {} job", jobName);
        } catch (Exception exception) {
            sendErrorTelemetryEvent(jobName, null, exception);
            log.error(String.format("Exception in job %s when started execution per-day, error %s",
                                    jobName,
                                    exception.getMessage()),
                      exception);
            throw exception;
        }

        // 6. Send a telemetry event for successful completion
        sendTelemetryEvent(COMPLETED_ALL_DAYS, jobName, null, stopwatch);
    }

    private void executeCleanupLogic(DatabaseCleanupExecutor<T> cleanupExecutor,
                                     String jobName,
                                     Instant day,
                                     int daysToKeep) {
        try {
            log.info("Processing database cleanup task {} for {} ", jobName, day.toString());
            sendTelemetryEvent(STARTED, jobName, day, null);

            T firstRecord = cleanupExecutor.getFirstRecord(day);
            if (firstRecord == null) {
                log.info("Database cleanup task {} for {}: no records found.", jobName, day);
                sendTelemetryEvent(SKIPPED_NO_DATA, jobName, day, null);
            } else {
                // If "day" is beyond a range that has to be kept in DB, initialize clean up logic for each
                // entity like request or instance. This clean up logic still marks mapper records as deleted
                // instead of removing them from DB, so day validation and deleting partition key after that section is still needed.
                if (day.isBefore(TimeUtils.getPreviousDate(daysToKeep))) {
                    log.info(
                            "Database cleanup task {} for {}: Date is beyond a TTL range ({} days), starting full cleanup",
                            jobName, day, daysToKeep);
                    cleanupExecutor.cleanupRecordsByDay(day);
                    sendTelemetryEvent(DELETED_ALL_RECORDS, jobName, day, null);
                    // continue with removing marked records
                }

                if (isAnyActiveRecordPerDay(cleanupExecutor, jobName, day)) {
                    log.info(
                            "Database cleanup task {} for {}: This day still has active records. Skipping.",
                            jobName,
                            day);
                    sendTelemetryEvent(SKIPPED_ACTIVE, jobName, day, null);
                } else {
                    log.info(
                            "Database cleanup task {} for {}: All records for this day are marked as deleted. Removing all records for this day from database.",
                            jobName,
                            day);
                    cleanupExecutor.deleteRecordsByDay(day);
                    sendTelemetryEvent(DELETED_DAY_DATA, jobName, day, null);
                }
            }
        } catch (Exception e) {
            String message = String.format(
                    "Database cleanup task %s for %s: Exception while running the job : %s",
                    jobName, day.toString(), e.getMessage());
            log.error(message, e);
            sendErrorTelemetryEvent(jobName, day, e);
            throw e;
        }
    }

    private boolean isAnyActiveRecordPerDay(DatabaseCleanupExecutor<T> cleanupExecutor, String jobName, Instant day) {
        String cursor = null;
        boolean hasData = true;
        int limit = icmsConfigurationProperties.getDatabaseCleanupDbPageSize(); // max records per call
        AtomicInteger activeCount = new AtomicInteger();
        AtomicInteger allCount = new AtomicInteger();

        while (hasData) {
            Slice<T> recordsPerDayPaged = cleanupExecutor.findRecordsPerDay(day, SliceResult.generateCassandraPageRequest(cursor, limit));
            recordsPerDayPaged.forEach(r -> {
                if (cleanupExecutor.isRecordActive(r)) {
                    activeCount.getAndIncrement();
                }
                allCount.getAndIncrement();
            });

            if (recordsPerDayPaged.hasNext()) {
                ByteBuffer pagingState = ((CassandraPageRequest) recordsPerDayPaged.getPageable()).getPagingState();
                cursor = toHexString(pagingState);
            }
            else {
                hasData = false;
            }
        }

        log.info(
                "Database cleanup task {} for {}: Active records: {}  from : {}.",
                jobName,
                day,
                activeCount.get(),
                allCount.get());

        return activeCount.get() > 0;
    }

    private void sendTelemetryEvent(String status, String jobName, Instant day, Stopwatch stopwatch) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(getDefaultMetadata(status, jobName, day, stopwatch))
                                                           .withEventName(Events.DATABASE_CLEANUP_TASK.toString())));
    }

    private void sendErrorTelemetryEvent(String jobName, Instant day, Exception e) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(getDefaultMetadata("FAILED", jobName, day, null))
                                                           .withError(e.getMessage())
                                                           .withEventName(Events.DATABASE_CLEANUP_TASK.toString())));
    }

    private Map<String, Object> getDefaultMetadata(String status, String jobName, Instant day, Stopwatch stopwatch) {
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.TASK_STATUS.getName(), status);
        metadata.put(EventMetaData.JOB_NAME.getName(), jobName);

        if (day != null) {
            metadata.put(EventMetaData.PROCESSED_DAY.getName(), day.toString());
        }

        if (stopwatch != null) {
            metadata.put(EventMetaData.EXECUTION_TIME.getName(),
                         stopwatch.elapsed(TimeUnit.SECONDS));
        }

        return metadata;
    }

}
