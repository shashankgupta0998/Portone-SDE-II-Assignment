// Package config reads runtime configuration from the environment.
package config

import (
	"errors"
	"os"
	"regexp"
)

// Config holds the settings shared by every subcommand.
type Config struct {
	DatabaseURL string
	Schema      string
}

var schemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Load reads DATABASE_URL (required) and RECON_SCHEMA (default "recon").
func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Schema: os.Getenv("RECON_SCHEMA")}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is not set (see .env.example)")
	}
	if c.Schema == "" {
		c.Schema = "recon"
	}
	if !ValidSchema(c.Schema) {
		return c, errors.New("RECON_SCHEMA must be a lower-case SQL identifier")
	}
	return c, nil
}

// ValidSchema reports whether s is safe to interpolate as a schema identifier.
func ValidSchema(s string) bool { return schemaName.MatchString(s) }
