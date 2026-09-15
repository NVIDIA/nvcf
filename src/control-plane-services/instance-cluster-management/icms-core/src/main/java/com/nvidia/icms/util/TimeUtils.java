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
package com.nvidia.icms.util;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import java.time.Instant;
import java.time.LocalDate;
import java.time.YearMonth;
import java.time.ZoneId;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Random;
import java.util.UUID;

public class TimeUtils {

    public static Instant getCurrentDate() {
        Instant instant = Instant.now();
        return instant.truncatedTo(ChronoUnit.DAYS);
    }

    public static Instant getFirstEpochDate() {
        return Instant.EPOCH;
    }

    public static Instant getPreviousDate(Integer daysToSubtract) {
        Instant instant = Instant.now().minus(daysToSubtract, ChronoUnit.DAYS);
        return instant.truncatedTo(ChronoUnit.DAYS);
    }

    public static Instant getFirstDateOfCurrentMonth() {
        LocalDate ld = YearMonth.from(Instant.now().atZone(ZoneId.of("UTC"))).atDay(1);
        return ld.atStartOfDay(ZoneId.of("UTC")).toInstant();
    }

    public static Instant getFirstDateOfPreviousMonth(Integer monthsToSubtract) {
        LocalDate ld = YearMonth.from(Instant.now().atZone(ZoneId.of("UTC"))).atDay(1);
        ld = ld.minusMonths(monthsToSubtract);
        return ld.atStartOfDay(ZoneId.of("UTC")).toInstant();
    }

    public static Instant getFirstDateOfMonthFromInstant(Instant instant) {
        LocalDate ld = YearMonth.from(instant.atZone(ZoneId.of("UTC"))).atDay(1);
        return ld.atStartOfDay(ZoneId.of("UTC")).toInstant();
    }

    public static Instant getCurrentDateUtc() {
        Instant instant = Instant.now().atZone(ZoneId.of("UTC")).toInstant();
        return instant.truncatedTo(ChronoUnit.DAYS);
    }

    public static Instant getDateFromInstant(Instant instant) {
        return instant.truncatedTo(ChronoUnit.DAYS);
    }

    public static Instant getSameDateOfPreviousMonth(Integer monthsToSubtract) {
        return getSameDateTimeOfPreviousMonth(monthsToSubtract)
                .truncatedTo(ChronoUnit.DAYS);
    }

    public static Instant getSameDateTimeOfPreviousMonth(Integer monthsToSubtract) {
        return Instant.now().atZone(ZoneId.of("UTC"))
                .minusMonths(monthsToSubtract)
                .toInstant();

    }

    public static Instant getNowTruncatedToMs() {
        Instant instant = Instant.now().atZone(ZoneId.of("UTC")).toInstant();
        return instant.truncatedTo(ChronoUnit.MILLIS);
    }


    public static Instant getInstantFromUuid(UUID timeBasedUuid) {
        long NUM_100NS_INTERVALS_SINCE_UUID_EPOCH = 0x01b21dd213814000L;
        long timestampMillis = timeBasedUuid.timestamp();
        long javaTimestamp = (timestampMillis - NUM_100NS_INTERVALS_SINCE_UUID_EPOCH) / 10000;

        return Instant.ofEpochMilli(javaTimestamp);
    }

    public static UUID getUuidFromTimeStamp(Instant timestamp) {
        Random random = new Random();
        return new UUID(Uuids.startOf(timestamp.toEpochMilli()).getMostSignificantBits(), random.nextLong());
    }

    public static UUID getTimeUuidNow() {
        return Uuids.timeBased();
    }

    public static List<PeriodOfTime> buildPeriodsForMonths(int numberOfPastMonths, int daysPerPeriod ) {
        Instant endDate = TimeUtils.getCurrentDateUtc();
        Instant startDate = TimeUtils.getSameDateOfPreviousMonth(numberOfPastMonths);

        return PeriodOfTime.buildPeriodsForDates(startDate, endDate, daysPerPeriod);
    }

    public static List<PeriodOfTime> buildPeriodsFromDate(Instant startDate, int daysPerPeriod ) {
        return PeriodOfTime.buildPeriodsForDates(startDate, TimeUtils.getCurrentDateUtc(), daysPerPeriod);
    }


}
