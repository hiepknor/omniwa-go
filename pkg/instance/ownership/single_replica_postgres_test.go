package ownership

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestPostgresGuardExcludesSecondProcess(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	firstDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := Acquire(ctx, firstDB)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(ctx, secondDB)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second guard=%v error=%v, want ErrAlreadyRunning", second, err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second, err = Acquire(ctx, secondDB)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
