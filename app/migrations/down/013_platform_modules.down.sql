-- Reverse 013 (commercial_members is untouched, so the pre-013 guard keeps working).
DROP FUNCTION IF EXISTS platform_tenants();
DROP TABLE IF EXISTS entitlement_audit;
DROP TABLE IF EXISTS user_modules;
DROP TABLE IF EXISTS platform_users;
DROP TABLE IF EXISTS platform_modules;
-- then, as postgres: DELETE FROM schema_migrations WHERE version='013_platform_modules';
