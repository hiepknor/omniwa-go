package netguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RequestSettings struct {
	AllowedHosts        []string
	AllowedPorts        []string
	AllowedContentTypes []string
	AllowPrivate        bool
	Timeout             time.Duration
	ConnectTimeout      time.Duration
	HeaderTimeout       time.Duration
	MaxRequestBytes     int64
	MaxResponseBytes    int64
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type Requester interface {
	Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*Response, error)
}

type requester struct {
	allowedHosts        map[string]struct{}
	allowedPorts        map[string]struct{}
	allowedContentTypes []string
	allowPrivate        bool
	maxRequestBytes     int64
	maxResponseBytes    int64
	client              *http.Client
	resolve             resolverFunc
	dial                dialFunc
}

func NewRequester(settings RequestSettings) (Requester, error) {
	connectTimeout := settings.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 5 * time.Second
	}
	return newRequester(settings, net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: connectTimeout}).DialContext)
}

func newRequester(settings RequestSettings, resolve resolverFunc, dial dialFunc) (*requester, error) {
	if settings.Timeout <= 0 || settings.MaxRequestBytes <= 0 || settings.MaxResponseBytes <= 0 {
		return nil, errors.New("outbound request timeout and byte limits must be positive")
	}
	if settings.HeaderTimeout <= 0 {
		settings.HeaderTimeout = 5 * time.Second
	}
	r := &requester{
		allowedHosts:        make(map[string]struct{}, len(settings.AllowedHosts)),
		allowedPorts:        make(map[string]struct{}, len(settings.AllowedPorts)+2),
		allowedContentTypes: append([]string(nil), settings.AllowedContentTypes...),
		allowPrivate:        settings.AllowPrivate,
		maxRequestBytes:     settings.MaxRequestBytes,
		maxResponseBytes:    settings.MaxResponseBytes,
		resolve:             resolve,
		dial:                dial,
	}
	for _, host := range settings.AllowedHosts {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if host != "" {
			r.allowedHosts[host] = struct{}{}
		}
	}
	if len(r.allowedHosts) == 0 {
		return nil, errors.New("outbound requester requires at least one exact host")
	}
	if len(settings.AllowedPorts) == 0 {
		settings.AllowedPorts = []string{"80", "443"}
	}
	for _, port := range settings.AllowedPorts {
		if port = strings.TrimSpace(port); port != "" {
			parsed, err := strconv.Atoi(port)
			if err != nil || parsed < 1 || parsed > 65535 {
				return nil, fmt.Errorf("invalid outbound port %q", port)
			}
			r.allowedPorts[port] = struct{}{}
		}
	}
	if len(r.allowedPorts) == 0 {
		return nil, errors.New("outbound requester requires at least one port")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = r.dialContext
	transport.ResponseHeaderTimeout = settings.HeaderTimeout
	r.client = &http.Client{
		Transport: transport,
		Timeout:   settings.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many outbound redirects")
			}
			return r.validateURL(req.Context(), req.URL)
		},
	}
	return r, nil
}

func (r *requester) Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*Response, error) {
	if int64(len(body)) > r.maxRequestBytes {
		return nil, errors.New("outbound request exceeds the configured size limit")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid outbound URL")
	}
	if err := r.validateURL(ctx, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create outbound request")
	}
	req.Header = header.Clone()
	resp, err := r.client.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			return nil, fmt.Errorf("outbound request failed: %w", urlError.Err)
		}
		return nil, errors.New("outbound request failed")
	}
	defer resp.Body.Close()
	if resp.ContentLength > r.maxResponseBytes {
		return nil, ErrResponseLarge
	}
	if len(r.allowedContentTypes) > 0 {
		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		allowed := false
		for _, expected := range r.allowedContentTypes {
			if strings.HasPrefix(contentType, strings.ToLower(expected)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errors.New("outbound response content type is not allowed")
		}
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, r.maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read outbound response")
	}
	if int64(len(responseBody)) > r.maxResponseBytes {
		return nil, ErrResponseLarge
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: responseBody}, nil
}

func (r *requester) validateURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return ErrUnsafeTarget
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if _, ok := r.allowedHosts[host]; !ok {
		return fmt.Errorf("%w: host is not configured", ErrUnsafeTarget)
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if _, ok := r.allowedPorts[port]; !ok {
		return fmt.Errorf("%w: port is not configured", ErrUnsafeTarget)
	}
	addresses, err := r.resolveHost(ctx, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("resolve outbound host")
	}
	if !r.allowPrivate {
		for _, address := range addresses {
			if !isPublicIP(address.IP) {
				return ErrUnsafeTarget
			}
		}
	}
	return nil
}

func (r *requester) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid outbound address")
	}
	addresses, err := r.resolveHost(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("resolve outbound host")
	}
	for _, address := range addresses {
		if !r.allowPrivate && !isPublicIP(address.IP) {
			return nil, ErrUnsafeTarget
		}
	}
	return r.dial(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
}

func (r *requester) resolveHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	return r.resolve(ctx, host)
}
