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

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import org.junit.jupiter.api.Test;

public class PeriodOfTimeTest {

    @Test
    void generate_1day() {
        //Arrange
        Instant dayNow = TimeUtils.getCurrentDate();
        Instant leftLimitDay = dayNow;

        //Act
        List<PeriodOfTime> periods = PeriodOfTime.buildQueryOptimizedPeriodsForDates(leftLimitDay, dayNow);

        //Assert
        assertEquals(1, periods.size());
        assertPeriod(periods.get(0), dayNow, 0, 0);
    }

    @Test
    void generate_6days() {
        //Arrange
        Instant dayNow = TimeUtils.getCurrentDate();
        Instant leftLimitDay = dayNow.minus(5 /* plus curent day */, ChronoUnit.DAYS);

        //Act
        List<PeriodOfTime> periods = PeriodOfTime.buildQueryOptimizedPeriodsForDates(leftLimitDay, dayNow);

        //Assert
        assertEquals(5, periods.size());
        assertPeriod(periods.get(0), dayNow, 0, 0);
        assertPeriod(periods.get(1), dayNow, 1, 1);
        assertPeriod(periods.get(2), dayNow, 2, 2);
        assertPeriod(periods.get(3), dayNow, 4, 3);
        assertPeriod(periods.get(4), dayNow, 5, 5);
    }

    @Test
    void generate_12days() {
        //Arrange
        Instant dayNow = TimeUtils.getCurrentDate();
        Instant leftLimitDay = dayNow.minus(11 /* plus 1 current one */, ChronoUnit.DAYS);

        //Act
        List<PeriodOfTime> periods = PeriodOfTime.buildQueryOptimizedPeriodsForDates(leftLimitDay, dayNow);

        //Assert
        assertEquals(6, periods.size());
        assertPeriod(periods.get(0), dayNow, 0, 0);
        assertPeriod(periods.get(1), dayNow, 1, 1);
        assertPeriod(periods.get(2), dayNow, 2, 2);
        assertPeriod(periods.get(3), dayNow, 4, 3);
        assertPeriod(periods.get(4), dayNow, 6, 5);
        assertPeriod(periods.get(5), dayNow, 11, 7);
    }

    @Test
    void generate_70days() {
        //Arrange
        Instant dayNow = TimeUtils.getCurrentDate();
        Instant leftLimitDay = dayNow.minus(69 /* plus 1 current one */, ChronoUnit.DAYS);

        //Act
        List<PeriodOfTime> periods = PeriodOfTime.buildQueryOptimizedPeriodsForDates(leftLimitDay, dayNow);

        //Assert
        assertEquals(10, periods.size());
        assertPeriod(periods.get(0), dayNow, 0, 0); // one day
        assertPeriod(periods.get(1), dayNow, 1, 1); // one day
        assertPeriod(periods.get(2), dayNow, 2, 2);// one day
        assertPeriod(periods.get(3), dayNow, 4, 3);// two days
        assertPeriod(periods.get(4), dayNow, 6, 5);// two days
        assertPeriod(periods.get(5), dayNow, 13, 7);//week
        assertPeriod(periods.get(6), dayNow, 20, 14);//week
        assertPeriod(periods.get(7), dayNow, 27, 21);//week
        assertPeriod(periods.get(8), dayNow, 59, 28);//month
        assertPeriod(periods.get(9), dayNow, 69, 60);//rest of the period
    }


    void assertPeriod(PeriodOfTime period, Instant dayNow, int leftOffset, int rightOffset) {
        assertEquals(dayNow.minus(leftOffset, ChronoUnit.DAYS), period.start());
        assertEquals(dayNow.minus(rightOffset, ChronoUnit.DAYS), period.end());
    }
}
