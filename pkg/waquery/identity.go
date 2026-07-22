package waquery

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type IdentityCacheSettings struct {
	PositiveTTL time.Duration
	NegativeTTL time.Duration
	StaleTTL    time.Duration
	MaxEntries  int
}

func DefaultIdentityCacheSettings() IdentityCacheSettings {
	return IdentityCacheSettings{PositiveTTL: 5 * time.Minute, NegativeTTL: 30 * time.Second, StaleTTL: 90 * time.Second, MaxEntries: 10_000}
}

type IdentityQuery func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error)

type IdentityResolver interface {
	Resolve(ctx context.Context, instanceID string, phones []string, query IdentityQuery) ([]types.IsOnWhatsAppResponse, error)
	RemoveInstance(instanceID string)
}

// IdentityReadResolver extends IdentityResolver for read-only callers that can
// safely return a bounded stale cache snapshot while the shared query guard is
// rate limited. Mutation and outbound callers continue to use Resolve and do
// not receive stale fallback data.
type IdentityReadResolver interface {
	IdentityResolver
	ResolveRead(ctx context.Context, instanceID string, phones []string, query IdentityQuery) (IdentityReadResult, error)
}

type IdentityReadResult struct {
	Responses []types.IsOnWhatsAppResponse
	Stale     bool
}

type CachedIdentityResolver struct {
	guard    Guard
	settings IdentityCacheSettings
	now      func() time.Time
	mu       sync.Mutex
	cache    map[string]*identityInstanceCache
}

type identityInstanceCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
}

type identityEntry struct {
	phone      string
	response   types.IsOnWhatsAppResponse
	expiresAt  time.Time
	staleUntil time.Time
}

func NewIdentityResolver(guard Guard, settings IdentityCacheSettings) (*CachedIdentityResolver, error) {
	if guard == nil {
		return nil, errors.New("query guard is required")
	}
	if settings.PositiveTTL <= 0 || settings.NegativeTTL <= 0 || settings.MaxEntries <= 0 {
		return nil, errors.New("identity cache TTLs and max entries must be positive")
	}
	if settings.StaleTTL < 0 {
		return nil, errors.New("identity stale cache TTL must not be negative")
	}
	if settings.StaleTTL == 0 {
		settings.StaleTTL = 90 * time.Second
	}
	return &CachedIdentityResolver{guard: guard, settings: settings, now: time.Now, cache: make(map[string]*identityInstanceCache)}, nil
}

func (r *CachedIdentityResolver) Resolve(ctx context.Context, instanceID string, phones []string, query IdentityQuery) ([]types.IsOnWhatsAppResponse, error) {
	result, err := r.resolve(ctx, instanceID, phones, query, false)
	return result.Responses, err
}

func (r *CachedIdentityResolver) ResolveRead(ctx context.Context, instanceID string, phones []string, query IdentityQuery) (IdentityReadResult, error) {
	return r.resolve(ctx, instanceID, phones, query, true)
}

func (r *CachedIdentityResolver) resolve(ctx context.Context, instanceID string, phones []string, query IdentityQuery, allowStale bool) (IdentityReadResult, error) {
	if ctx == nil || strings.TrimSpace(instanceID) == "" || query == nil {
		return IdentityReadResult{}, errors.New("identity query context, instance ID, and callback are required")
	}
	if len(phones) == 0 {
		return IdentityReadResult{Responses: []types.IsOnWhatsAppResponse{}}, nil
	}

	cache := r.instance(instanceID)
	now := r.now()
	found, stale, missing := cache.lookup(phones, now)
	resultIsStale := allowStale && guardCircuitUnavailable(r.guard, instanceID)
	if len(missing) > 0 {
		fresh, err := Do(ctx, r.guard, instanceID, OperationUserExists, ResourceKey(missing...), func(queryCtx context.Context) ([]types.IsOnWhatsAppResponse, error) {
			return query(queryCtx, missing)
		})
		if err != nil {
			if !allowStale || !isRateLimitError(err) || !mergeCompleteStale(found, stale, missing) {
				return IdentityReadResult{}, err
			}
			resultIsStale = true
		} else {
			cache.store(fresh, now, r.settings)
			for _, response := range fresh {
				found[response.Query] = response
			}
		}
	}

	result := make([]types.IsOnWhatsAppResponse, 0, len(phones))
	for _, phone := range phones {
		if response, ok := found[phone]; ok {
			result = append(result, response)
		}
	}
	return IdentityReadResult{Responses: result, Stale: resultIsStale}, nil
}

func guardCircuitUnavailable(guard Guard, instanceID string) bool {
	snapshot, ok := guard.Snapshot(instanceID)
	return ok && (snapshot.CircuitState == CircuitOpen || snapshot.CircuitState == CircuitHalfOpen)
}

func isRateLimitError(err error) bool {
	var rateLimit *RateLimitError
	return errors.As(err, &rateLimit)
}

func mergeCompleteStale(found, stale map[string]types.IsOnWhatsAppResponse, missing []string) bool {
	for _, phone := range missing {
		response, ok := stale[phone]
		if !ok {
			return false
		}
		found[phone] = response
	}
	return true
}

func (r *CachedIdentityResolver) RemoveInstance(instanceID string) {
	r.mu.Lock()
	delete(r.cache, instanceID)
	r.mu.Unlock()
}

func (r *CachedIdentityResolver) instance(instanceID string) *identityInstanceCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	cache := r.cache[instanceID]
	if cache == nil {
		cache = &identityInstanceCache{entries: make(map[string]*list.Element), lru: list.New()}
		r.cache[instanceID] = cache
	}
	return cache
}

func (c *identityInstanceCache) lookup(phones []string, now time.Time) (map[string]types.IsOnWhatsAppResponse, map[string]types.IsOnWhatsAppResponse, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := make(map[string]types.IsOnWhatsAppResponse, len(phones))
	stale := make(map[string]types.IsOnWhatsAppResponse, len(phones))
	missing := make([]string, 0, len(phones))
	for _, phone := range phones {
		element := c.entries[phone]
		if element == nil {
			missing = append(missing, phone)
			continue
		}
		entry := element.Value.(identityEntry)
		if !now.Before(entry.expiresAt) {
			if !now.Before(entry.staleUntil) {
				c.lru.Remove(element)
				delete(c.entries, phone)
				missing = append(missing, phone)
				continue
			}
			stale[phone] = entry.response
			missing = append(missing, phone)
			continue
		}
		c.lru.MoveToFront(element)
		found[phone] = entry.response
	}
	return found, stale, missing
}

func (c *identityInstanceCache) store(responses []types.IsOnWhatsAppResponse, now time.Time, settings IdentityCacheSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, response := range responses {
		if response.Query == "" {
			continue
		}
		ttl := settings.NegativeTTL
		if response.IsIn {
			ttl = settings.PositiveTTL
		}
		expiresAt := now.Add(ttl)
		entry := identityEntry{phone: response.Query, response: response, expiresAt: expiresAt, staleUntil: expiresAt.Add(settings.StaleTTL)}
		if element := c.entries[response.Query]; element != nil {
			element.Value = entry
			c.lru.MoveToFront(element)
		} else {
			c.entries[response.Query] = c.lru.PushFront(entry)
		}
	}
	for c.lru.Len() > settings.MaxEntries {
		element := c.lru.Back()
		delete(c.entries, element.Value.(identityEntry).phone)
		c.lru.Remove(element)
	}
}
