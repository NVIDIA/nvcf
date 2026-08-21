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

import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

/**
 * Worker authentication registration payload sent by NVCA alongside instance status updates.
 * ICMS stores this as the authoritative worker-identity set for the instance and uses it
 * during token introspection to match worker-presented JWTs.
 */
@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
public class WorkerAuth {

    /** Worker ServiceAccount subject (SAT) or SPIFFE ID (SPIFFE). */
    @NotBlank
    private String sub;

    /** ServiceAccount UID (SAT flow only; absent for SPIFFE). */
    @Nullable
    private String saUid;

    /** Pod name + UID pairs (SAT) or SPIFFE ID + worker UUID pairs (SPIFFE). */
    @NotNull
    @NotEmpty
    @Valid
    private List<WorkerIdentifier> workerIdentifiers;
}
