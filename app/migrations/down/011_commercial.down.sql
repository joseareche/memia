-- Reverse of 011 (run as devbrain_owner; then DELETE FROM schema_migrations WHERE version='011_commercial').
-- Back up first: pg_dump -Fc -t 'quote*' -t 'commercial_*' devbrain > commercial-before-down.dump
UPDATE api_tokens SET scopes = array_remove(scopes, 'commercial') WHERE name = 'web-chat';
DROP TABLE IF EXISTS quote_events, quote_versions, quote_items, quotes, commercial_members, commercial_customers, commercial_series, commercial_companies, commercial_settings;
