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
package com.nvidia.nvcf.configuration.nats;

/** Low-cardinality cause of a NATS connection failure, safe to use as a metric tag. */
public enum NatsFailureReason {
    TLS_CERT_EXPIRED("tls_cert_expired"),
    TLS_VERIFICATION("tls_verification"),
    TLS("tls"),
    AUTHENTICATION("authentication"),
    TIMEOUT("timeout"),
    CONNECTION("connection"),
    OTHER("other");

    private final String tag;

    NatsFailureReason(String tag) {
        this.tag = tag;
    }

    public String tag() {
        return tag;
    }
}
