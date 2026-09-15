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
package com.nvidia.nvcf.service.function;

import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.eventledger.EventLedgerClient;
import java.time.Clock;
import java.time.Instant;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class FunctionStatusTransitionService {

    private final FunctionsRepository functionsRepository;
    private final EventLedgerClient eventLedgerClient;
    // Inject the time source so transition timestamps can be controlled in tests.
    private final Clock clock;

    public void persist(
            FunctionEntity function, UUID deploymentId, FunctionStatus newStatus) {
        var previousStatus = function.getFunctionStatus();
        if (previousStatus == newStatus) {
            return;
        }
        function.setFunctionStatus(newStatus);
        functionsRepository.insert(function);
        eventLedgerClient.publish(
                function.getNcaId(),
                function.getFunctionId(),
                function.getFunctionVersionId(),
                deploymentId,
                previousStatus,
                newStatus,
                Instant.now(clock));
    }
}
