package projection_repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"go.mau.fi/whatsmeow/types"
)

func TestContactLIDMappingResolverPostgres(t *testing.T) {
	dsn, phone, expectedLID := os.Getenv("TEST_AUTH_POSTGRES_DSN"), os.Getenv("TEST_AUTH_PN"), os.Getenv("TEST_AUTH_LID")
	if dsn == "" || phone == "" || expectedLID == "" {
		t.Skip("TEST_AUTH_POSTGRES_DSN, TEST_AUTH_PN, and TEST_AUTH_LID are required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resolver := NewContactLIDMappingResolver(db)
	lid, err := resolver.GetLIDForPN(context.Background(), types.NewJID(phone, types.DefaultUserServer))
	if err != nil || lid != types.NewJID(expectedLID, types.HiddenUserServer) {
		t.Fatalf("PostgreSQL PN lookup = %s, %v", lid, err)
	}
}

func TestContactLIDMappingResolverReadsBothDirections(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resolver := NewContactLIDMappingResolver(db)

	mock.ExpectQuery("SELECT lid FROM whatsmeow_lid_map WHERE pn=\\$1").WithArgs("15550001").
		WillReturnRows(sqlmock.NewRows([]string{"lid"}).AddRow("9000001"))
	lid, err := resolver.GetLIDForPN(context.Background(), types.NewJID("15550001", types.DefaultUserServer))
	if err != nil || lid != types.NewJID("9000001", types.HiddenUserServer) {
		t.Fatalf("PN lookup = %s, %v", lid, err)
	}

	mock.ExpectQuery("SELECT pn FROM whatsmeow_lid_map WHERE lid=\\$1").WithArgs("9000001").
		WillReturnRows(sqlmock.NewRows([]string{"pn"}).AddRow("15550001"))
	pn, err := resolver.GetPNForLID(context.Background(), types.NewJID("9000001", types.HiddenUserServer))
	if err != nil || pn != types.NewJID("15550001", types.DefaultUserServer) {
		t.Fatalf("LID lookup = %s, %v", pn, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContactLIDMappingResolverDistinguishesMissingAndUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resolver := NewContactLIDMappingResolver(db)
	pn := types.NewJID("15550001", types.DefaultUserServer)

	mock.ExpectQuery("SELECT lid FROM whatsmeow_lid_map WHERE pn=\\$1").WithArgs("15550001").
		WillReturnError(sql.ErrNoRows)
	if lid, lookupErr := resolver.GetLIDForPN(context.Background(), pn); lookupErr != nil || !lid.IsEmpty() {
		t.Fatalf("missing lookup = %s, %v", lid, lookupErr)
	}
	mock.ExpectQuery("SELECT lid FROM whatsmeow_lid_map WHERE pn=\\$1").WithArgs("15550001").
		WillReturnError(errors.New("offline"))
	if _, lookupErr := resolver.GetLIDForPN(context.Background(), pn); lookupErr == nil {
		t.Fatal("expected storage failure")
	}
}
