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
package com.nvidia.ess.it;

import java.util.Locale;
import org.apache.commons.lang3.RandomStringUtils;

/**
 * One parameterized execution of an integration-test template. Carries the axes along which an IT may
 * vary without changing the assertions it makes.
 *
 * Current parameter-list (test-case execution variation dimensions):
 *
 * <ul>
 *   <li>{@link NonNotaryAuthorizationTypeInDB} — the value forced into the
 *       {@code authorization.type} UDT column; all auth must be completely independent of this.</li>
 * </ul>
 */
public record ITExecutionVariant(NonNotaryAuthorizationTypeInDB nonNotaryAuthorizationTypeInDB) {

    /**
     * No-op baseline test-execution variant: no {@code authorization.type} value is written to the DB
     * (i.e. same as written by the creation APIs).
     */
    public static final ITExecutionVariant DEFAULT =
            new ITExecutionVariant(NonNotaryAuthorizationTypeInDB.NULL);

    /**
     * Maps a base namespace to a namespace unique to this variant (each IT execution variant gets its
     * own ESS namespace). Ordered state-changes to the DB contents of the ESS namespace(s) in each
     * variant are preserved and decoupled from those of other variants.
     */
    public String effectiveNs(String baseNamespace) {
        if (this.equals(DEFAULT)) {
            return baseNamespace;
        }
        return String.format("%s-%s", baseNamespace,
                nonNotaryAuthorizationTypeInDB.name().toLowerCase(Locale.ROOT));
    }

    public String displayName() {
        return String.format("typeInDb=%s", nonNotaryAuthorizationTypeInDB.name());
    }

    /** Type of value to write into the {@code authorization.type} UDT field. */
    public enum NonNotaryAuthorizationTypeInDB {
        OAUTH,
        NULL,
        RANDOM;

        /** The concrete DB-field value to write. */
        public String valueInDB() {
            return switch (this) {
                case OAUTH -> "OAUTH";
                case NULL -> null;
                case RANDOM -> RandomStringUtils.secure().nextAlphanumeric(10);
            };
        }
    }
}
