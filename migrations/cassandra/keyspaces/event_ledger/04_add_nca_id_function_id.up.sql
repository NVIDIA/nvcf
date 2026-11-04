-- Add account scoping columns to the v3 event tables.
--
-- nca_id and function_id are first-class columns so account-scoped reads can
-- filter on them without parsing context or details. Both are optional while
-- producers roll out; rows written before this migration keep NULLs and are not
-- exposed by account-scoped reads. There is no backfill.
-- Fresh installs get these columns from 03_init_tables.up.sql, while this
-- migration upgrades existing keyspaces created before the columns existed.

ALTER TABLE event_ledger.events_v3 ADD IF NOT EXISTS (nca_id text, function_id text);
ALTER TABLE event_ledger.stats_v3 ADD IF NOT EXISTS (nca_id text, function_id text);
