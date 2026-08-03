-- SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
BEGIN BATCH

INSERT INTO test.credentials_by_customer_and_timestamp (
customer,
truncated_ts,
credential_id,
created_at,
credential_type,
encrypted_credential
) VALUES (
'dummy_customer_1',
'1970-01-01 00:00:00.000000+0000',
'cred-1',
'2022-12-13 10:05:47.954000+0000',
'CREDENTIAL_TYPE_USER_PASS',
'encrypted-cred-1'
);


INSERT INTO test.credential_by_id (
credential_id,
customer,
truncated_ts
) VALUES (
'cred-1',
'dummy_customer_1',
'1970-01-01 00:00:00.000000+0000'
);

INSERT INTO test.credentials_by_customer_and_timestamp (
customer,
truncated_ts,
credential_id,
created_at,
credential_type,
encrypted_credential
) VALUES (
'dummy_customer_1',
'1970-01-01 00:00:00.000000+0000',
'cred-2',
'2022-12-13 10:05:47.954000+0000',
'CREDENTIAL_TYPE_USER_PASS',
'encrypted-cred-2'
);


INSERT INTO test.credential_by_id (
credential_id,
customer,
truncated_ts
) VALUES (
'cred-2',
'dummy_customer_1',
'1970-01-01 00:00:00.000000+0000'
);

INSERT INTO test.credentials_by_customer_and_timestamp (
customer,
truncated_ts,
credential_id,
created_at,
credential_type,
encrypted_credential
) VALUES (
'dummy_customer_2',
'1970-01-01 00:00:00.000000+0000',
'cred-3',
'2022-12-13 10:05:47.954000+0000',
'CREDENTIAL_TYPE_USER_PASS',
'encrypted-cred-3'
);


INSERT INTO test.credential_by_id (
credential_id,
customer,
truncated_ts
) VALUES (
'cred-3',
'dummy_customer_2',
'1970-01-01 00:00:00.000000+0000'
);

APPLY BATCH
;
