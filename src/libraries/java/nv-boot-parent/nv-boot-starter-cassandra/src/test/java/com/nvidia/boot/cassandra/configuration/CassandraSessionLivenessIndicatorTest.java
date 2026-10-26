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
package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.time.ZoneOffset;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.Status;

class CassandraSessionLivenessIndicatorTest {

    private static final Duration GRACE = Duration.ofMinutes(3);

    private final MutableClock clock = new MutableClock();
    private boolean sessionWorks;
    private boolean freshSessionWorks;
    private final CassandraSessionLivenessIndicator indicator = new CassandraSessionLivenessIndicator(
            () -> sessionWorks, () -> freshSessionWorks, GRACE, clock);

    @Test
    void upWhenSessionWorks() {
        sessionWorks = true;
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void staysUpForAnyLengthOfCassandraOutage() {
        sessionWorks = false;
        freshSessionWorks = false;
        for (int i = 0; i < 10; i++) {
            indicator.evaluate();
            clock.advance(Duration.ofMinutes(5));
        }
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void downOnlyAfterStuckForGracePeriod() {
        sessionWorks = false;
        freshSessionWorks = true;
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);

        clock.advance(GRACE.minusSeconds(1));
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);

        clock.advance(Duration.ofSeconds(1));
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.DOWN);
    }

    @Test
    void recoversWhenSessionReconnects() {
        sessionWorks = false;
        freshSessionWorks = true;
        indicator.evaluate();
        clock.advance(GRACE);
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.DOWN);

        sessionWorks = true;
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void outageDuringStuckWindowRestartsTheGracePeriod() {
        sessionWorks = false;
        freshSessionWorks = true;
        indicator.evaluate();
        clock.advance(Duration.ofMinutes(2));

        freshSessionWorks = false;
        indicator.evaluate();
        clock.advance(Duration.ofMinutes(2));

        freshSessionWorks = true;
        indicator.evaluate();
        clock.advance(Duration.ofMinutes(2));
        indicator.evaluate();
        assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
    }

    private static final class MutableClock extends Clock {
        private Instant now = Instant.parse("2026-01-01T00:00:00Z");

        void advance(Duration duration) {
            now = now.plus(duration);
        }

        @Override
        public ZoneId getZone() {
            return ZoneOffset.UTC;
        }

        @Override
        public Clock withZone(ZoneId zone) {
            return this;
        }

        @Override
        public Instant instant() {
            return now;
        }
    }
}
