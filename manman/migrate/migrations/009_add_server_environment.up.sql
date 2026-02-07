-- Add environment field to servers table for better organization
-- This helps group servers by deployment environment (dev, staging, prod, etc.)

ALTER TABLE servers
ADD COLUMN environment VARCHAR(100);

CREATE INDEX idx_servers_environment ON servers(environment) WHERE environment IS NOT NULL;

-- Add comment for documentation
COMMENT ON COLUMN servers.environment IS 'Optional deployment environment (e.g., dev, staging, production)';
