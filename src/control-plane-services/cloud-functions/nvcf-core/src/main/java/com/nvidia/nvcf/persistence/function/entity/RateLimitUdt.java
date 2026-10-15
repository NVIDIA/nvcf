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
package com.nvidia.nvcf.persistence.function.entity;

import jakarta.annotation.Nullable;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@NoArgsConstructor
@Builder(toBuilder = true)
@UserDefinedType(RateLimitUdt.USER_DEFINED_TYPE_NAME)
public class RateLimitUdt {
    public static final String USER_DEFINED_TYPE_NAME = "ratelimit_udt_v2";

    @Nullable
    @Column("rate")
    private String rate;

    @Nullable
    @Column("exempted_nca_ids")
    private Set<String> exemptedNcaIds;

    @Nullable
    @Column("per_nca_id_rate")
    private Map<String, String> perNcaIdRate;

    @Nullable
    @Column("sync_check")
    private Boolean syncCheck;

    @Nullable
    @Column("per_user_rate")
    private String perUserRate;

    public boolean isEmpty() {
        return this.getRate() == null
                && (this.getExemptedNcaIds() == null || this.getExemptedNcaIds().isEmpty())
                && this.getSyncCheck() == null
                && (this.getPerNcaIdRate() == null || this.getPerNcaIdRate().isEmpty())
                && this.getPerUserRate() == null;
    }

}
