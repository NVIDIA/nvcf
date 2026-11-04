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

import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.List;

/**
 * Defines a period of time between @start and @end
 * @param start value of @Instant for start of the period
 * @param end value of @Instant for end of the period
 */
public record PeriodOfTime(Instant start, Instant end) {

    /**
     * Generates a list for period of time and each period has @daysPerPeriod days except the last one (it can have less)
     * @param startDate Start of split time frame
     * @param endDate End of split time frame
     * @param daysPerPeriod Defines a number of days per period
     * @return List of periods of time
     */
    public static List<PeriodOfTime> buildPeriodsForDates(Instant startDate, Instant endDate, int daysPerPeriod) {
        ArrayList<PeriodOfTime> startOfPeriod = new ArrayList<>();

        startDate = startDate.truncatedTo(ChronoUnit.DAYS);
        endDate = endDate.truncatedTo(ChronoUnit.DAYS).plus(1, ChronoUnit.DAYS);

        while (startDate.isBefore(endDate)) {
            Instant periodEndDate = startDate.plus(daysPerPeriod - 1, ChronoUnit.DAYS);
            if (periodEndDate.isAfter(endDate)) {
                periodEndDate = endDate;
            }

            startOfPeriod.add( new PeriodOfTime(startDate, periodEndDate));
            startDate = periodEndDate.plus(1, ChronoUnit.DAYS);
        }

        return startOfPeriod;
    }

    /**
     * Generates a list of periods optimized for DB query. periods will have:
     *    Last week :
     *          First 3 days (EndDay, EndDay-1, EndDay-2) : one day per period
     *          Next 4 days : 2 days per period : [EndDay-4, EndDay -3], [EndDay-6, EndDay -5]
     *    Next 3 weeks : 7 days per period [EndDy-13, EndDay -7], [EndDy-20, EndDay -14], [EndDy-28, EndDay -21]
     *    Next month - 32 days per period [EndDay-59, EndDay-29]
     *    Above covers 60 days
     *    Everything else in a single period  [EndDay-..., EndDay-61]
     *
     *    startDate <= endDate
     * @param startDate
     * @param endDate
     * @return
     */
    public static List<PeriodOfTime> buildQueryOptimizedPeriodsForDates(Instant startDate, Instant endDate) {
        ArrayList<PeriodOfTime> periods = new ArrayList<>();
        endDate = endDate.truncatedTo(ChronoUnit.DAYS);
        startDate = startDate.truncatedTo(ChronoUnit.DAYS);

        Instant dayRightLimit = endDate;

        // Generate one day period for 3 days
        Instant dayLeftLimit = getDayWithOffsetDesc(dayRightLimit, 2 /* it includes the current day */, startDate);
        if (!generatePeriodsDesc(periods, dayLeftLimit, dayRightLimit, 1)) {
            return periods;
        }

        // Generate 2 days per period for next 4 days
        dayRightLimit = dayLeftLimit.minus(1, ChronoUnit.DAYS);
        dayLeftLimit = getDayWithOffsetDesc(dayRightLimit, 3 /* 4 days actually */, startDate);
        if (!generatePeriodsDesc(periods, dayLeftLimit, dayRightLimit, 2)) {
            return periods;
        }

        // Generate 7 days per period for next 3 weeks
        dayRightLimit = dayLeftLimit.minus(1, ChronoUnit.DAYS);
        dayLeftLimit = getDayWithOffsetDesc(dayRightLimit, 20 /* 21 days actually */, startDate);
        if (!generatePeriodsDesc(periods, dayLeftLimit, dayRightLimit, 7)) {
            return periods;
        }

        // Generate 32 days per period for next month
        dayRightLimit = dayLeftLimit.minus(1, ChronoUnit.DAYS);
        dayLeftLimit = getDayWithOffsetDesc(dayRightLimit, 31 /* 32 days actually*/ , startDate);
        if (!generatePeriodsDesc(periods, dayLeftLimit, dayRightLimit, 32)) {
            return periods;
        }

        // Generate rest of days in the  for next month
        dayRightLimit = dayLeftLimit.minus(1, ChronoUnit.DAYS);
        dayLeftLimit = getDayWithOffsetDesc(dayRightLimit, 365, startDate);
        generatePeriodsDesc(periods, dayLeftLimit, dayRightLimit, 365);

        return periods;

    }


    /**
     * startDate <= endDate
     * @param dest
     * @param startDate
     * @param endDate
     * @param maxDaysPerPeriod
     * @return returns "left" day that was used
     */

    private static boolean generatePeriodsDesc(@NotNull List<PeriodOfTime> dest, @NotNull Instant startDate, @NotNull Instant endDate, int maxDaysPerPeriod) {
        startDate = startDate.truncatedTo(ChronoUnit.DAYS);
        Instant prevDayBeforeStart = startDate.minus(1, ChronoUnit.DAYS);
        Instant currDay = endDate.truncatedTo(ChronoUnit.DAYS);
        boolean result = false;

        while (prevDayBeforeStart.isBefore(currDay)) {
            Instant periodStartDay = currDay.minus(maxDaysPerPeriod - 1, ChronoUnit.DAYS);
            if (startDate.isAfter(periodStartDay)) {
                periodStartDay = startDate.truncatedTo(ChronoUnit.DAYS);
            }
            dest.add( new PeriodOfTime(periodStartDay, currDay));
            currDay = periodStartDay.minus(1, ChronoUnit.DAYS);
            result = true;
        }

        return result;
    }

    private static Instant getDayWithOffsetDesc(Instant startDay, int daysOffset, Instant dayLeftLimit) {
        Instant result = startDay.minus(daysOffset, ChronoUnit.DAYS);
        if (result.isBefore(dayLeftLimit)) {
            result = dayLeftLimit;
        }
        return result;
    }

}
