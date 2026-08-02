package ownership

import (
	"context"
	"database/sql"
	"testing"
)

func TestNewSideEffectFencerRejectsInvalidDependencies(t *testing.T) {
	if _, err := NewSideEffectFencer(nil, 1); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewSideEffectFencer(&sql.DB{}, 0); err == nil {
		t.Fatal("non-positive epoch was accepted")
	}
}

func TestNilSideEffectFencerFailsClosed(t *testing.T) {
	var fencer *SideEffectFencer
	if err := fencer.Do(context.Background(), func(context.Context) error { return nil }); err == nil {
		t.Fatal("nil fencer admitted an operation")
	}
}

func TestSideEffectFencerRejectsMissingOperation(t *testing.T) {
	fencer := &SideEffectFencer{db: &sql.DB{}, epoch: 1}
	if err := fencer.Do(context.Background(), nil); err == nil {
		t.Fatal("nil operation was accepted")
	}
}
