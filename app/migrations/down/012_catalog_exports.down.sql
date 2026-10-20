ALTER TABLE commercial_companies DROP COLUMN IF EXISTS logo_sha256, DROP COLUMN IF EXISTS default_terms;
DROP TABLE IF EXISTS integration_providers, quote_exports, commercial_blobs, price_versions, service_catalog CASCADE;
DROP FUNCTION IF EXISTS price_versions_no_overlap();
