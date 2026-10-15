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

package com.nvidia.apikeys.converters;

import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class KeyOwnerVoBuilder {

    private final MillisecondPrecisionClock clock;

    public KeyOwnerVo getNewKeyOwnerVo(KeyOwnerType keyOwnerType, String actorId) {
        return KeyOwnerVo.builder()
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .ownerId(actorId)
                .ownerType(keyOwnerType)
                // status updated set to minimum possible value
                // this will trigger validation whether the user is active
                .ownerStatusUpdatedAt(clock.instant().truncatedTo(ChronoUnit.SECONDS))
                .build();
    }

    public KeyOwnerVo getKeyOwnerVoFromExistingKey(KeyByOwnerAndServiceVo key) {
        return KeyOwnerVo.builder()
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .ownerStatus(key.getOwnerStatus())
                .ownerStatusUpdatedAt(key.getOwnerStatusUpdatedAt())
                .build();
    }

    public KeyOwnerVo getKeyOwnerVoFromModel(KeyByOwnerAndServiceModel model) {
        return KeyOwnerVo.builder()
                .ownerType(model.getOwnerType())
                .ownerId(model.getOwnerId())
                .ownerStatus(model.getOwnerStatus())
                .ownerStatusUpdatedAt(model.getOwnerStatusUpdatedAt())
                .build();
    }

}
