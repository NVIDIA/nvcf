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

package com.nvidia.icms.inbound.rest.model.workers;

import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

/**
 * Worker identity registration carried on the instance status update.
 *
 * <p>The worker ServiceAccount name is fixed ({@link #WORKER_SA_NAME}); the instance is
 * anchored by the namespace and the ServiceAccount UID, so both are mandatory.</p>
 */
@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
public class WorkerAuth {

    public static final String WORKER_SA_NAME = "nvcf-worker";
    public static final int MAX_WORKER_IDENTIFIERS = 64;
    public static final String UUID_PATTERN =
            "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$";

    /** Worker ServiceAccount subject: {@code system:serviceaccount:<namespace>:nvcf-worker}. */
    @NotBlank
    @Size(max = 512)
    private String sub;

    /** Kubernetes namespace the worker ServiceAccount and pods live in. */
    @NotBlank
    @Size(max = 253)
    private String namespace;

    /** ServiceAccount UID; matched against {@code kubernetes.io.serviceaccount.uid}. */
    @NotBlank
    @Pattern(regexp = UUID_PATTERN)
    private String saUid;

    /** Pod name + UID pairs (SAT) or SPIFFE ID + worker UUID pairs (SPIFFE). */
    @NotNull
    @NotEmpty
    @Size(max = MAX_WORKER_IDENTIFIERS)
    @Valid
    private List<WorkerIdentifier> workerIdentifiers;

    /** The subject the fixed worker ServiceAccount has in {@code namespace}. */
    public static String expectedSubject(String namespace) {
        return "system:serviceaccount:" + namespace + ":" + WORKER_SA_NAME;
    }
}
