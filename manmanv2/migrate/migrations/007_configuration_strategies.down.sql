-- Rollback configuration strategies migration
DROP TABLE IF EXISTS configuration_patches CASCADE;
DROP TABLE IF EXISTS strategy_parameter_bindings CASCADE;
DROP TABLE IF EXISTS configuration_strategies CASCADE;
