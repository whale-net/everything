// Package catalog provides the sensor chip catalog and a database seeder
// derived from chips.yaml.
//
// The YAML file is the single source of truth for supported chip models and
// their I2C address variants.  The Seeder function performs idempotent upserts
// so it is safe to run on every migrate invocation.
package catalog

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed chips.yaml
var chipsYAML []byte

// Chip describes a physical sensor chip model.
type Chip struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Addresses   []Address `yaml:"addresses"`
}

// Address is one I2C address variant for a chip.
type Address struct {
	I2CAddress int    `yaml:"i2c_address"`
	IsDefault  bool   `yaml:"is_default"`
	AddrConfig string `yaml:"addr_config"`
	CPPLabel   string `yaml:"cpp_label"`
}

type catalog struct {
	Chips []Chip `yaml:"chips"`
}

// Load parses chips.yaml and returns the chip list.
func Load() ([]Chip, error) {
	var c catalog
	if err := yaml.Unmarshal(chipsYAML, &c); err != nil {
		return nil, fmt.Errorf("parse chips.yaml: %w", err)
	}
	return c.Chips, nil
}

// Seeder returns a migrate.Seeder-compatible function that upserts the chip
// catalog into sensor_chip and sensor_chip_address.
//
// Compatible with libs/go/migrate.WithSeeder — pass the result directly:
//
//	migrate.RunCLI(fs, "migrations", migrate.WithSeeder(catalog.Seeder()))
func Seeder() func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		chips, err := Load()
		if err != nil {
			return err
		}
		return seed(ctx, db, chips)
	}
}

func seed(ctx context.Context, db *sql.DB, chips []Chip) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, chip := range chips {
		var chipID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO sensor_chip (name, description)
			VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description
			RETURNING sensor_chip_id
		`, chip.Name, chip.Description).Scan(&chipID)
		if err != nil {
			return fmt.Errorf("upsert chip %s: %w", chip.Name, err)
		}

		for _, addr := range chip.Addresses {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO sensor_chip_address
					(sensor_chip_id, i2c_address, is_default, addr_config)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (sensor_chip_id, i2c_address)
				DO UPDATE SET
					is_default  = EXCLUDED.is_default,
					addr_config = EXCLUDED.addr_config
			`, chipID, addr.I2CAddress, addr.IsDefault, addr.AddrConfig)
			if err != nil {
				return fmt.Errorf("upsert address 0x%02X for chip %s: %w",
					addr.I2CAddress, chip.Name, err)
			}
		}
	}

	return tx.Commit()
}
