package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Policy string

const (
	PolicyDisabled   Policy = "disabled"
	PolicyPublicOnly Policy = "public_only"
	PolicyAllowlist  Policy = "allowlist"
)

var (
	ErrDisabled      = errors.New("remote media fetching is disabled")
	ErrUnsafeTarget  = errors.New("remote media target is not allowed")
	ErrResponseLarge = errors.New("remote media response exceeds the configured size limit")
)

type Settings struct {
	Policy       Policy
	AllowedHosts []string
	Timeout      time.Duration
	MaxBytes     int64
}

type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) ([]byte, error)
}

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

type fetcher struct {
	policy       Policy
	allowedHosts map[string]struct{}
	maxBytes     int64
	client       *http.Client
	resolve      resolverFunc
	dial         dialFunc
}

func NewFetcher(settings Settings) (Fetcher, error) {
	return newFetcher(settings, net.DefaultResolver.LookupIPAddr, (&net.Dialer{}).DialContext)
}

func newFetcher(settings Settings, resolve resolverFunc, dial dialFunc) (*fetcher, error) {
	if settings.Policy == "" {
		settings.Policy = PolicyPublicOnly
	}
	if settings.Policy != PolicyDisabled && settings.Policy != PolicyPublicOnly && settings.Policy != PolicyAllowlist {
		return nil, fmt.Errorf("invalid remote media fetch policy %q", settings.Policy)
	}
	if settings.Timeout <= 0 {
		return nil, errors.New("remote media fetch timeout must be positive")
	}
	if settings.MaxBytes <= 0 {
		return nil, errors.New("remote media maximum response size must be positive")
	}

	f := &fetcher{
		policy:       settings.Policy,
		allowedHosts: make(map[string]struct{}, len(settings.AllowedHosts)),
		maxBytes:     settings.MaxBytes,
		resolve:      resolve,
		dial:         dial,
	}
	for _, host := range settings.AllowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			f.allowedHosts[host] = struct{}{}
		}
	}
	if f.policy == PolicyAllowlist && len(f.allowedHosts) == 0 {
		return nil, errors.New("remote media allowlist policy requires at least one host")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = f.dialContext
	f.client = &http.Client{
		Transport: transport,
		Timeout:   settings.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many remote media redirects")
			}
			return f.validateURL(req.Context(), req.URL)
		},
	}
	return f, nil
}

func (f *fetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	if f.policy == PolicyDisabled {
		return nil, ErrDisabled
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote media URL: %w", err)
	}
	if err := f.validateURL(ctx, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create remote media request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			return nil, fmt.Errorf("fetch remote media: %w", urlError.Err)
		}
		return nil, fmt.Errorf("fetch remote media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("remote media server returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > f.maxBytes {
		return nil, ErrResponseLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read remote media response: %w", err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, ErrResponseLarge
	}
	return data, nil
}

func (f *fetcher) validateURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only HTTP and HTTPS are supported", ErrUnsafeTarget)
	}
	if u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: URL credentials and empty hosts are forbidden", ErrUnsafeTarget)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if f.policy == PolicyAllowlist {
		if _, ok := f.allowedHosts[host]; !ok {
			return fmt.Errorf("%w: host is not allowlisted", ErrUnsafeTarget)
		}
	}
	addresses, err := f.resolveHost(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve remote media host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("resolve remote media host: no addresses returned")
	}
	for _, address := range addresses {
		if !isPublicIP(address.IP) {
			return fmt.Errorf("%w: host resolves to a non-public address", ErrUnsafeTarget)
		}
	}
	return nil
}

func (f *fetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid remote media address: %w", err)
	}
	addresses, err := f.resolveHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve remote media host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("resolve remote media host: no addresses returned")
	}
	for _, resolved := range addresses {
		if !isPublicIP(resolved.IP) {
			return nil, fmt.Errorf("%w: host resolves to a non-public address", ErrUnsafeTarget)
		}
	}
	return f.dial(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

func (f *fetcher) resolveHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	return f.resolve(ctx, host)
}

func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedPublicSpecialRanges {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var blockedPublicSpecialRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}
