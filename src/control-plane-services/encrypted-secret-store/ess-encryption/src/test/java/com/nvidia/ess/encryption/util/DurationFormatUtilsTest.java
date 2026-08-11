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
package com.nvidia.ess.encryption.util;

import java.time.Duration;
import java.util.stream.Stream;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

class DurationFormatUtilsTest {

    private static Stream<Arguments> durationFormatArguments() {
        return Stream.of(Duration.ZERO, Duration.ofDays(1), Duration.ofDays(2))
                .flatMap(days -> Stream.of(Duration.ZERO, Duration.ofHours(1), Duration.ofHours(2))
                        .flatMap(hours -> Stream.of(Duration.ZERO, Duration.ofMinutes(1),
                                        Duration.ofMinutes(2))
                                .flatMap(minutes -> Stream.of(Duration.ZERO, Duration.ofSeconds(1),
                                                Duration.ofSeconds(2))
                                        .flatMap(seconds -> Stream.of(Duration.ZERO,
                                                        Duration.ofMillis(1), Duration.ofMillis(2))
                                                .map(millis -> {
                                                    Duration fullDuration =
                                                            days.plus(hours).plus(minutes)
                                                                    .plus(seconds)
                                                                    .plus(millis);
                                                    if (fullDuration.isZero()) {
                                                        return Arguments.of(fullDuration, "000 milliseconds");
                                                    }
                                                    String formattedString;
                                                    if (seconds.isZero() && !millis.isZero()) {
                                                        String tmp = org.apache.commons.lang3.time.DurationFormatUtils.formatDurationWords(
                                                                fullDuration.plus(Duration.ofSeconds(1)).toMillis(), true,
                                                                true);
                                                        formattedString = Strings.CS.replaceOnce(tmp, "1 second", "0 seconds");
                                                    } else {
                                                        formattedString =
                                                                org.apache.commons.lang3.time.DurationFormatUtils.formatDurationWords(
                                                                        fullDuration.toMillis(), true,
                                                                        true);
                                                    }

                                                    if (seconds.isZero() && !millis.isZero() && days.isZero() && hours.isZero() && minutes.isZero()) {
                                                        formattedString = Strings.CS.replaceOnce(formattedString, "0 seconds", StringUtils.EMPTY);
                                                    }

                                                    if (!millis.isZero()) {
                                                        if (millis.compareTo(Duration.ofMillis(1)) == 0) {
                                                            formattedString += " 001 millisecond";
                                                        } else {
                                                            formattedString += " " + StringUtils.leftPad(Long.toString(millis.toMillisPart()), 3, '0') + " milliseconds";
                                                        }
                                                    }
                                                    return Arguments.of(fullDuration,
                                                            formattedString.stripLeading());
                                                })))));
    }


    @ParameterizedTest
    @MethodSource("durationFormatArguments")
    void formatDurationWords(Duration duration, String expectedString) {
        Assertions.assertEquals(expectedString,
                DurationFormatUtils.formatDurationWords(duration.toMillis()));
    }
}
