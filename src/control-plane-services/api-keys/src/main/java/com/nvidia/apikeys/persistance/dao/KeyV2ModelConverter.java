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

package com.nvidia.apikeys.persistance.dao;

import com.nvidia.apikeys.persistance.models.KeyV2Model;
import com.nvidia.apikeys.persistance.models.KeysByOwnerAndAccountAndServiceModel;
import com.nvidia.apikeys.validators.KeyExpirationValidator;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Converts keys_v2 and keys_by_owner_and_account_and_service rows to and from their value
 * objects. Reads apply the expiration validator, so an active key past its expiry is returned as
 * expired.
 */
@Service
@RequiredArgsConstructor
public class KeyV2ModelConverter {

    private final EncryptedModelConverter<KeyV2Model, KeyV2Vo> keyConverter;
    private final EncryptedModelConverter<KeysByOwnerAndAccountAndServiceModel,
            KeysByOwnerAndAccountAndServiceVo> keysByOwnerAndAccountAndServiceConverter;
    private final KeyExpirationValidator expirationValidator;

    public KeyV2Model voToModel(KeyV2Vo vo) {
        return keyConverter.voToModel(vo);
    }

    public KeyV2Vo modelToVo(KeyV2Model model) {
        return expirationValidator.validateStatus(keyConverter.modelToVo(model));
    }

    public KeysByOwnerAndAccountAndServiceModel voToModel(KeysByOwnerAndAccountAndServiceVo vo) {
        return keysByOwnerAndAccountAndServiceConverter.voToModel(vo);
    }

    public KeysByOwnerAndAccountAndServiceVo modelToVo(KeysByOwnerAndAccountAndServiceModel model) {
        return expirationValidator.validateStatus(
                keysByOwnerAndAccountAndServiceConverter.modelToVo(model));
    }
}
