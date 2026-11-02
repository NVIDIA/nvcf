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

import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.services.LegacyMigrationState.REFRESH_INTERVAL;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.services.LegacyMigrationState.Phase;
import java.time.Clock;
import java.time.Instant;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class LegacyMigrationStateTest {

    private static final Instant AFTER_REFRESH = TEST_TIME.plus(REFRESH_INTERVAL);

    @Mock
    private LegacyKeyMaintenance maintenance;
    @Mock
    private Clock clock;

    private LegacyMigrationState state;

    @BeforeEach
    void setUp() {
        state = new LegacyMigrationState(maintenance, clock);
        when(clock.instant()).thenReturn(TEST_TIME);
    }

    @Test
    void noMarkerAndNoLeaseIsBefore() {
        assertThat(state.current()).isEqualTo(Phase.BEFORE);
        assertThat(state.isCompleted()).isFalse();
    }

    @Test
    void leaseWithoutMarkerIsDuring() {
        when(maintenance.isRunning()).thenReturn(true);

        assertThat(state.current()).isEqualTo(Phase.DURING);
    }

    @Test
    void markerIsAfter() {
        when(maintenance.isCompleted()).thenReturn(true);

        assertThat(state.current()).isEqualTo(Phase.AFTER);
        assertThat(state.isCompleted()).isTrue();
        verify(maintenance, never()).isRunning();
    }

    @Test
    void afterIsCachedForever() {
        when(maintenance.isCompleted()).thenReturn(true);
        state.current();

        assertThat(state.current()).isEqualTo(Phase.AFTER);
        verify(maintenance, times(1)).isCompleted();
        verify(clock, times(1)).instant();
    }

    @Test
    void beforeIsCachedUntilRefresh() {
        state.current();
        when(maintenance.isCompleted()).thenReturn(true);

        when(clock.instant()).thenReturn(AFTER_REFRESH.minusMillis(1));
        assertThat(state.current()).isEqualTo(Phase.BEFORE);

        when(clock.instant()).thenReturn(AFTER_REFRESH);
        assertThat(state.current()).isEqualTo(Phase.AFTER);
        verify(maintenance, times(2)).isCompleted();
    }

    @Test
    void duringMovesToAfterOnRefresh() {
        when(maintenance.isRunning()).thenReturn(true);
        assertThat(state.current()).isEqualTo(Phase.DURING);

        when(maintenance.isCompleted()).thenReturn(true);
        when(clock.instant()).thenReturn(AFTER_REFRESH);

        assertThat(state.current()).isEqualTo(Phase.AFTER);
    }

    @Test
    void ownerStatusIsNotMigratedWithoutItsMarker() {
        assertThat(state.isOwnerStatusMigrated()).isFalse();
        verify(maintenance, never()).isCompleted();
    }

    @Test
    void ownerStatusMarkerIsCachedForever() {
        when(maintenance.isOwnerStatusCompleted()).thenReturn(true);

        assertThat(state.isOwnerStatusMigrated()).isTrue();
        assertThat(state.isOwnerStatusMigrated()).isTrue();
        verify(maintenance, times(1)).isOwnerStatusCompleted();
    }

    @Test
    void missingOwnerStatusMarkerIsReadAgainAfterRefresh() {
        state.isOwnerStatusMigrated();
        when(maintenance.isOwnerStatusCompleted()).thenReturn(true);

        when(clock.instant()).thenReturn(AFTER_REFRESH.minusMillis(1));
        assertThat(state.isOwnerStatusMigrated()).isFalse();

        when(clock.instant()).thenReturn(AFTER_REFRESH);
        assertThat(state.isOwnerStatusMigrated()).isTrue();
    }

    @Test
    void failedOwnerStatusMarkerReadIsNotMigrated() {
        when(maintenance.isOwnerStatusCompleted()).thenThrow(new IllegalStateException("db down"));

        assertThat(state.isOwnerStatusMigrated()).isFalse();
    }

    @Test
    void failedFirstReadIsBefore() {
        when(maintenance.isCompleted()).thenThrow(new IllegalStateException("db down"));

        assertThat(state.current()).isEqualTo(Phase.BEFORE);
    }

    @Test
    void failedReadKeepsLastKnownPhase() {
        when(maintenance.isRunning()).thenReturn(true);
        state.current();

        when(maintenance.isCompleted()).thenThrow(new IllegalStateException("db down"));
        when(clock.instant()).thenReturn(AFTER_REFRESH);

        assertThat(state.current()).isEqualTo(Phase.DURING);
    }
}
