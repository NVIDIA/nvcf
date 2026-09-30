-- SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0

-- Discover owner status across accounts independently of key records.
CREATE CUSTOM INDEX IF NOT EXISTS owner_status_by_account_owner_idx
    ON api_keys_api.owner_status_by_account (owner_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS owner_status_by_account_and_service_owner_idx
    ON api_keys_api.owner_status_by_account_and_service (owner_id)
    USING 'StorageAttachedIndex';
