-- Add the per-reservation backup opt-out column.
--
-- Fresh installs get this column from 03_init_tables.up.sql, while this migration
-- upgrades existing keyspaces created before the column existed.
-- A null value means backup is allowed, so rows written before this migration keep
-- the previous behaviour of falling back to another cluster when the primary is unhealthy.

ALTER TABLE sis_api.reservations ADD IF NOT EXISTS reservation_backup_disabled boolean;
