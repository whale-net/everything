-- Add 'volume' to configuration strategy types
ALTER TABLE configuration_strategies 
DROP CONSTRAINT IF EXISTS configuration_strategies_strategy_type_check;

ALTER TABLE configuration_strategies
ADD CONSTRAINT configuration_strategies_strategy_type_check 
CHECK (strategy_type IN (
    'cli_args', 
    'env_vars', 
    'file_properties', 
    'file_json', 
    'file_yaml', 
    'file_ini', 
    'file_xml', 
    'file_lua', 
    'file_custom',
    'volume'
));

COMMENT ON COLUMN configuration_strategies.strategy_type IS 'Type of configuration strategy. "volume" indicates a persistent volume mount.';
