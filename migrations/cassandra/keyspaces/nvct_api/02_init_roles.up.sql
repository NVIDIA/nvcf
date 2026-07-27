CREATE ROLE IF NOT EXISTS nvct_api_app_access;
GRANT SELECT, MODIFY on keyspace nvct_api to nvct_api_app_access;
GRANT SELECT on keyspace system to nvct_api_app_access;

CREATE ROLE IF NOT EXISTS nvct_api_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('nvct_api_app_access', 'nvct_api_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'nvct_api_app_access'} where role = 'nvct_api_app_v0';
