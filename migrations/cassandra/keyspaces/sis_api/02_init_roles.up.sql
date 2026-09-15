CREATE ROLE IF NOT EXISTS sis_api_app_access;
GRANT SELECT, MODIFY on keyspace sis_api to sis_api_app_access;
GRANT SELECT on keyspace system to sis_api_app_access;

CREATE ROLE IF NOT EXISTS sis_api_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('sis_api_app_access', 'sis_api_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'sis_api_app_access'} where role = 'sis_api_app_v0';
