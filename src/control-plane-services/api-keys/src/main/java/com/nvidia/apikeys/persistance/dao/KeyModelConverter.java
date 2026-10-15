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

import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.validators.KeyExpirationValidator;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * This class wraps the converter and adds expiration validator on read.
 */
@Service
@RequiredArgsConstructor
public class KeyModelConverter {

    private final EncryptedModelConverter<KeyModel, KeyVo> converter;
    private final KeyExpirationValidator expirationValidator;


    public KeyModel voToModel(KeyVo vo) {
        return converter.voToModel(vo);
    }

    public KeyVo modelToVo(KeyModel model) {
        KeyVo keyVo = converter.modelToVo(model);
        return expirationValidator.validateStatus(keyVo);
    }

}
