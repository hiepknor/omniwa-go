package projection_repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/types"
)

// ContactLIDMappingResolver reads the authoritative mapping table maintained by
// Whatsmeow. It deliberately bypasses the live client's in-memory cache.
type ContactLIDMappingResolver struct {
	db *sql.DB
}

func NewContactLIDMappingResolver(db *sql.DB) *ContactLIDMappingResolver {
	return &ContactLIDMappingResolver{db: db}
}

func (r *ContactLIDMappingResolver) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if r == nil || r.db == nil || ctx == nil || pn.Server != types.DefaultUserServer || pn.User == "" {
		return types.JID{}, errors.New("valid local PN mapping lookup is required")
	}
	return r.lookup(ctx, "SELECT lid FROM whatsmeow_lid_map WHERE pn=$1", pn.User, types.HiddenUserServer)
}

func (r *ContactLIDMappingResolver) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if r == nil || r.db == nil || ctx == nil || lid.Server != types.HiddenUserServer || lid.User == "" {
		return types.JID{}, errors.New("valid local LID mapping lookup is required")
	}
	return r.lookup(ctx, "SELECT pn FROM whatsmeow_lid_map WHERE lid=$1", lid.User, types.DefaultUserServer)
}

func (r *ContactLIDMappingResolver) lookup(ctx context.Context, query, source, targetServer string) (types.JID, error) {
	var target string
	err := r.db.QueryRowContext(ctx, query, source).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return types.JID{}, nil
	}
	if err != nil {
		return types.JID{}, fmt.Errorf("read local LID mapping: %w", err)
	}
	if target == "" {
		return types.JID{}, errors.New("local LID mapping contains an empty identity")
	}
	return types.NewJID(target, targetServer), nil
}
