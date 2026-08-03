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

package com.nvidia.apikeys.services;

import static com.nvidia.apikeys.vo.KeyOwnerType.USER;


import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * This services role is to retrieve and update current user status.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class SynchronousUserStatusService {

    private final KeysDao keysDao;

    /**
     * Returns last known user details if present.
     *
     * @param userId - id of the user whose status is loaded.
     * @return last known details about the user.
     */
    public Optional<KeyOwnerVo> getLastKnownStatus(String userId) {
        log.info("loading user {} info", userId);
        return keysDao.getKeyOwner(USER, userId);
    }

}
