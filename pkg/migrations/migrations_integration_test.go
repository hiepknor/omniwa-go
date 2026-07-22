package migrations

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRunAppliesPendingMigrationInOneLockedTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(advisoryLockKey).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS schema_migrations").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "schema_migrations" ORDER BY version ASC`)).WillReturnRows(
		sqlmock.NewRows([]string{"version", "name", "checksum", "applied_at"}),
	)
	mock.ExpectExec("CREATE TABLE projection_states").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO \"schema_migrations\"").
		WithArgs(registry[0].Name, migrationChecksum(registry[0]), sqlmock.AnyArg(), registry[0].Version).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(registry[0].Version))
	mock.ExpectCommit()

	if err := Run(db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
