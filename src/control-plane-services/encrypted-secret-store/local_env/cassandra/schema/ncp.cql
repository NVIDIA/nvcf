-- SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

-- setup nvcf namespace for ncp, entity type and onboard non-Notary and Notary clients
-- Also change the created and update timestamp at the time of insertion
INSERT INTO ess.namespaces (
    namespace,
    entity_types,
    created_at,
    updated_at,
    entity_hash_size,
    require_lwt_for_secret_version_writes,
    oauth_authorizations,
    notary_authorizations)
    VALUES (
        'nvcf',
        {'functions': {name: 'functions', deleted_at: null}, 'accounts': {name: 'accounts', deleted_at: null}, 'tasks': {name: 'tasks', deleted_at: null}},
        '2025-05-25 15:01:19.002000+0000',
        '2025-05-25 15:01:19.002000+0000',
        10,
        False,
        {'ncp-api': {id: 'ncp-api', name: 'nvcf api service client', jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ess-api/jwt/jwks', issuer: 'http://ess-api.ess.svc.cluster.local',  type: 'OAUTH'}},
        {'ncp-api': {id: 'ncp-api', name: 'nvcf notary client',      jwks_url: 'http://openbao-server.vault-system.svc.cluster.local:8200/v1/services/ncp-api/jwt/jwks', issuer: 'http://ncp-api.ncp.svc.cluster.local',  type: 'NOTARY'}}
    );