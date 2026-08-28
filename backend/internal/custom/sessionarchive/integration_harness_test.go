//go:build integration

package sessionarchive

import (
	"context"
	"database/sql"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

var sessionArchiveIntegrationDB *sql.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	command := exec.CommandContext(ctx, "docker", "info")
	command.Env = os.Environ()
	if command.Run() != nil {
		if os.Getenv("CI") != "" {
			log.Print("docker is not available (CI=true); failing session archive integration tests")
			os.Exit(1)
		}
		log.Print("docker is not available; skipping session archive integration tests")
		os.Exit(0)
	}
	image := strings.TrimSpace(os.Getenv("SUB2API_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("session_archive_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("start session archive postgres container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = container.Terminate(ctx) }()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("resolve session archive postgres DSN: %v", err)
		os.Exit(1)
	}
	sessionArchiveIntegrationDB, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("open session archive postgres: %v", err)
		os.Exit(1)
	}
	if err := sessionArchiveIntegrationDB.PingContext(ctx); err != nil {
		log.Printf("ping session archive postgres: %v", err)
		os.Exit(1)
	}
	_, err = sessionArchiveIntegrationDB.ExecContext(ctx, `
		CREATE TABLE session_archive_pg_objects (
			object_key TEXT PRIMARY KEY,
			total_bytes BIGINT NOT NULL CHECK (total_bytes>=0),
			chunk_count INT NOT NULL CHECK (chunk_count>=0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE session_archive_pg_object_chunks (
			object_key TEXT NOT NULL REFERENCES session_archive_pg_objects(object_key) ON DELETE CASCADE,
			sequence_no INT NOT NULL CHECK (sequence_no>=0),
			data BYTEA NOT NULL CHECK (octet_length(data)<=8388608),
			PRIMARY KEY (object_key,sequence_no)
		);`)
	if err != nil {
		log.Printf("create session archive postgres store schema: %v", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = sessionArchiveIntegrationDB.Close()
	os.Exit(code)
}
