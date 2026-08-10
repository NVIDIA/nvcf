CREATE ROLE IF NOT EXISTS event_ledger_app_access;
GRANT SELECT, MODIFY on keyspace event_ledger to event_ledger_app_access;
GRANT SELECT on keyspace system to event_ledger_app_access;

CREATE ROLE IF NOT EXISTS event_ledger_app_v0 with login = true and password = '${SERVICE_ROLE_PASSWORD}';

INSERT INTO system_auth.role_members (role, member) VALUES ('event_ledger_app_access', 'event_ledger_app_v0');
UPDATE system_auth.roles SET member_of = member_of + {'event_ledger_app_access'} where role = 'event_ledger_app_v0';
