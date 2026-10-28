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

package com.nvidia.nvcf.configuration.exceptions;

import com.nvidia.boot.exceptions.ServiceUnavailableException;

/**
 * Cassandra failures that are safe to report as retriable.
 *
 * <p>Only failures where Cassandra did no work are: a coordinator read timeout, too few replicas
 * alive, an overloaded or bootstrapping coordinator, or no node reachable. Write timeouts and
 * driver timeouts are not, because the write may have been applied and a retry of a create would
 * duplicate it. Read and write failures are not transient.
 */
public final class StorageExceptions {

    public static final String STORAGE_UNAVAILABLE = "Storage is temporarily unavailable";

    private StorageExceptions() {
    }

    public static ServiceUnavailableException storageUnavailable(Throwable cause) {
        return new ServiceUnavailableException(STORAGE_UNAVAILABLE, cause);
    }
}
