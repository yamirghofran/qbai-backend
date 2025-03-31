package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB holds the database connection pool and queries
type DB struct {
	Pool    *pgxpool.Pool
	Queries *Queries
}

// NewDB creates a new DB instance
func NewDB(ctx context.Context) (*DB, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	return &DB{
		Pool:    pool,
		Queries: New(pool),
	}, nil
}

// Close closes the database connection
func (db *DB) Close() {
	db.Pool.Close()
}
