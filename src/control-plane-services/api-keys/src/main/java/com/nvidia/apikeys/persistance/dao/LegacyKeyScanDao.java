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
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import java.nio.ByteBuffer;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.stereotype.Service;

/**
 * Full scan of the legacy keys table for the legacy key migration.
 *
 * <p>Decrypts with the raw converter instead of {@link KeyModelConverter}, so the stored key
 * status is copied as it is and not rewritten to EXPIRED on read.
 */
@Service
@RequiredArgsConstructor
public class LegacyKeyScanDao {

    private final KeyRepository keyRepository;
    private final EncryptedModelConverter<KeyModel, KeyVo> converter;

    /**
     * Rows are returned encrypted so the caller can count a row that fails to decrypt as one
     * failed key instead of failing the page.
     */
    public Page page(int limit, ByteBuffer pagingState) {
        var slice = keyRepository.findAll(CassandraPageRequest.of(PageRequest.of(0, limit), pagingState));
        ByteBuffer next = null;
        if (slice.hasNext()) {
            next = ((CassandraPageRequest) slice.getPageable()).getPagingState();
        }
        return new Page(slice.getContent(), next);
    }

    public KeyVo decrypt(KeyModel model) {
        return converter.modelToVo(model);
    }

    /**
     * @param next paging state of the next page, or null after the last page
     */
    public record Page(List<KeyModel> rows, ByteBuffer next) {

        public boolean hasNext() {
            return next != null;
        }
    }
}
