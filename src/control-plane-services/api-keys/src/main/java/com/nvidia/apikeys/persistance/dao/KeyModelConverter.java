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

import com.nvidia.apikeys.persistance.models.KeyByAccountAndOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.validators.KeyExpirationValidator;
import com.nvidia.apikeys.vo.KeyByAccountAndOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Converts every key table row to and from its value object. Reads apply the expiration
 * validator, so an active key past its expiry is returned as expired.
 */
@Service
@RequiredArgsConstructor
public class KeyModelConverter {

    private final EncryptedModelConverter<KeyModel, KeyVo> keyConverter;
    private final EncryptedModelConverter<KeyByOwnerAndServiceModel, KeyByOwnerAndServiceVo>
            keyByOwnerAndServiceConverter;
    private final EncryptedModelConverter<KeyByAccountAndOwnerAndServiceModel,
            KeyByAccountAndOwnerAndServiceVo> keyByAccountAndOwnerAndServiceConverter;
    private final KeyExpirationValidator expirationValidator;

    public KeyModel voToModel(KeyVo vo) {
        return keyConverter.voToModel(vo);
    }

    public KeyVo modelToVo(KeyModel model) {
        return expirationValidator.validateStatus(keyConverter.modelToVo(model));
    }

    public KeyByOwnerAndServiceModel voToModel(KeyByOwnerAndServiceVo vo) {
        return keyByOwnerAndServiceConverter.voToModel(vo);
    }

    public KeyByOwnerAndServiceVo modelToVo(KeyByOwnerAndServiceModel model) {
        return expirationValidator.validateStatus(keyByOwnerAndServiceConverter.modelToVo(model));
    }

    public KeyByAccountAndOwnerAndServiceModel voToModel(KeyByAccountAndOwnerAndServiceVo vo) {
        return keyByAccountAndOwnerAndServiceConverter.voToModel(vo);
    }

    public KeyByAccountAndOwnerAndServiceVo modelToVo(KeyByAccountAndOwnerAndServiceModel model) {
        return expirationValidator.validateStatus(
                keyByAccountAndOwnerAndServiceConverter.modelToVo(model));
    }
}
