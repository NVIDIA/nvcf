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
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Our storage does not support time resolution beyond millisecond. This class is a wrapper that
 * produces time truncated to millis in order to simplify time handling and eliminate the need to
 * discard time difference within millisecond.
 */
@Service
@RequiredArgsConstructor
public class MillisecondPrecisionClock {

    private final Clock clock;

    public Instant instant() {
        return clock.instant().truncatedTo(ChronoUnit.MILLIS);
    }

}
