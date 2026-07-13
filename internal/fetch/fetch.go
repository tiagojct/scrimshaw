package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 10 << 20

type Client struct{ HTTP *http.Client }

func New(timeout time.Duration) *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: safeDialContext(dialer)}
	return &Client{HTTP: &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: checkRedirect}}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	// req.Response is the response that triggered this redirect (see net/http's
	// Client.do), so this is the only place a permanent (301/308) hop is
	// visible; withRedirectTracking's context value records it for
	// GetTrackingRedirects to report back to the caller.
	if info, ok := req.Context().Value(redirectKey{}).(*redirectInfo); ok && req.Response != nil {
		if req.Response.StatusCode == http.StatusMovedPermanently || req.Response.StatusCode == http.StatusPermanentRedirect {
			info.sawPermanent = true
		}
	}
	return ValidateURL(req.URL.String())
}

type redirectKey struct{}

type redirectInfo struct{ sawPermanent bool }

func withRedirectTracking(ctx context.Context) (context.Context, *redirectInfo) {
	info := &redirectInfo{}
	return context.WithValue(ctx, redirectKey{}, info), info
}

func safeDialContext(d *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := publicAddresses(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("invalid HTTP URL")
	}
	return nil
}

func ValidateHost(ctx context.Context, host string) error {
	_, err := publicAddresses(ctx, host)
	return err
}

func publicAddresses(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if forbidden(ip) {
			return nil, errors.New("private address denied")
		}
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no addresses")
	}
	public := make([]net.IP, 0, len(ips))
	for _, candidate := range ips {
		if forbidden(candidate.IP) {
			return nil, fmt.Errorf("address %s denied", candidate.IP)
		}
		public = append(public, candidate.IP)
	}
	return public, nil
}

func forbidden(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.IsMulticast() || cgnat.Contains(ip)
}

func (c *Client) Get(ctx context.Context, rawURL, etag, modified string) ([]byte, http.Header, error) {
	body, headers, _, err := c.get(ctx, rawURL, etag, modified, false)
	return body, headers, err
}

func (c *Client) GetMedia(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	body, headers, _, err := c.get(ctx, rawURL, "", "", true)
	return body, headers, err
}

// GetTrackingRedirects behaves like Get, but also reports the final URL
// actually reached and whether a permanent (301/308) redirect occurred
// anywhere along the way. Used by the feed poller to detect a feed that has
// permanently moved, so it can update the stored URL instead of letting the
// failure counter climb toward auto-disable.
func (c *Client) GetTrackingRedirects(ctx context.Context, rawURL, etag, modified string) (body []byte, headers http.Header, finalURL string, permanent bool, err error) {
	ctx, info := withRedirectTracking(ctx)
	body, headers, finalURL, err = c.get(ctx, rawURL, etag, modified, false)
	return body, headers, finalURL, info.sawPermanent, err
}

// Status performs an SSRF-guarded request and returns the final HTTP status
// code without reading the body. Used by the dead-link checker.
func (c *Client) Status(ctx context.Context, rawURL string) (int, error) {
	if err := ValidateURL(rawURL); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Scrimshaw/1.0 (+https://github.com/tiagojct/scrimshaw)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (c *Client) get(ctx context.Context, rawURL, etag, modified string, allowMedia bool) ([]byte, http.Header, string, error) {
	if err := ValidateURL(rawURL); err != nil {
		return nil, nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", "Scrimshaw/1.0 (+https://github.com/tiagojct/scrimshaw)")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header, finalURL, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, finalURL, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, finalURL, err
	}
	if len(body) > maxResponseBytes {
		return nil, nil, finalURL, errors.New("response exceeds size limit")
	}
	if !allowMedia && !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/") && !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "xml") && !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") && resp.Header.Get("Content-Type") != "" {
		return nil, nil, finalURL, errors.New("unsupported content type")
	}
	return body, resp.Header, finalURL, nil
}
