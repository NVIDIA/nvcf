ALTER TABLE sis_api.requests ADD IF NOT EXISTS creation_bucket timestamp;
ALTER TABLE sis_api.instances ADD IF NOT EXISTS creation_bucket timestamp;
