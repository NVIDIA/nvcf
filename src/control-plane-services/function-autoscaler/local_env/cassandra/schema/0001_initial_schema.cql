# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

DROP KEYSPACE IF EXISTS nvcf_autoscaler;

CREATE KEYSPACE IF NOT EXISTS nvcf_autoscaler WITH REPLICATION = {'class' : 'SimpleStrategy', 'replication_factor' : 1 };

CREATE TABLE IF NOT EXISTS nvcf_autoscaler.recently_invoked_functions (
    function_id uuid,
    function_version_id uuid,
    nca_id_string text,  -- Regular column, not part of PK (text, not uuid - comes from TimeSeriesDB as base64)
    last_updated_at timestamp,
    PRIMARY KEY ((function_id, function_version_id))
) WITH default_time_to_live = 600;

CREATE TABLE IF NOT EXISTS nvcf_autoscaler.locks (
    lock_name text PRIMARY KEY,
    node_id text,
    acquired_at timestamp
) WITH default_time_to_live = 600;

CREATE TABLE IF NOT EXISTS nvcf_autoscaler.healthy_nodes (
    node_id text PRIMARY KEY,
    last_updated_at timestamp,
) WITH default_time_to_live = 180;
