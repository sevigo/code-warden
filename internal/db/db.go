package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"

	// import db drivers
	_ "github.com/lib/pq"

	"github.com/sevigo/code-warden/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB is a wrapper around the sqlx.DB connection pool.
type DB struct {
	*sqlx.DB
}

// NewDatabase establishes a connection to the PostgreSQL database using the
// provided configuration and verifies the connection with a ping.
func NewDatabase(cfg *config.DBConfig) (*DB, func(), error) {
	if cfg == nil {
		return nil, func() {}, errors.New("database config is required")
	}

	conn, err := sqlx.Connect("postgres", cfg.DSN)
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to connect to database: %w", err)
	}

	conn.SetMaxOpenConns(cfg.MaxOpenConns)
	conn.SetMaxIdleConns(cfg.MaxIdleConns)
	conn.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	conn.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, func() {}, fmt.Errorf("failed to ping database: %w", err)
	}

	db := &DB{
		DB: conn,
	}

	return db, func() {
		if err := conn.Close(); err != nil {
			slog.Error("failed to close database connection", "error", err)
		}
	}, nil
}

// RunMigrations executes pending database migrations embedded in the binary.
// It also handles cases where a previous migration failed, leaving the database
// in a "dirty" state.
func (db *DB) RunMigrations() error {
	migrator, err := db.newMigrator()
	if err != nil {
		return err
	}

	// Check the current migration version and if the schema is dirty.
	_, dirty, err := migrator.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migration version: %w", err)
	}

	if dirty {
		return fmt.Errorf("failed to apply migrations: database is in dirty state. You might need to manually fix it (e.g., 'migrate force <version>') or check logs for previous migration errors")
	}

	err = migrator.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

// newMigrator creates a new migrate instance using the embedded migration files.
func (db *DB) newMigrator() (*migrate.Migrate, error) {
	sourceDriver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to create migration source: %w", err)
	}

	dbDriver, err := postgres.WithInstance(db.DB.DB, &postgres.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to create database driver: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}
	return migrator, nil
}
