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
package com.nvidia.boot.cassandra.retry;

import com.datastax.oss.driver.api.core.context.DriverContext;

/**
 * {@link NextHostRetryPolicy} that also retries a first read timeout when the coordinator had
 * data but fewer responses than required.
 *
 * <p>A range read (a secondary or SAI index lookup) always has data present from the
 * coordinator's own replica, so when a replica is down the timeout only shows as fewer
 * responses than required, never as missing data. Cassandra does not retry range reads on
 * another replica, so this policy retries them on the next host from the client.
 *
 * <p>This is opt-in. {@link NextHostRetryPolicy}, which most services use, does not retry
 * these timeouts, because that would also retry single-partition read timeouts. Select this
 * policy per service with {@code spring.cassandra.advanced.retry-policy.class}.
 */
public class RangeReadRetryPolicy extends NextHostRetryPolicy {

    public RangeReadRetryPolicy(DriverContext context, String profileName) {
        super(context, profileName);
    }

    @Override
    protected boolean shouldRetryReadTimeout(
            int blockFor,
            int received,
            boolean dataPresent,
            int retryCount) {
        return retryCount == 0 && (!dataPresent || received < blockFor);
    }
}
