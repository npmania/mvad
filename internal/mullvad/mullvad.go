// Package mullvad is a small client for the Mullvad public API.
package mullvad

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const DefaultBaseURL = "https://api.mullvad.net"

const (
	maxRespBytes   = 8 << 20
	maxDetailBytes = 256
)

func limitBody(r io.Reader) io.Reader { return io.LimitReader(r, maxRespBytes) }

type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu     sync.Mutex
	tokens map[string]token
}

type token struct {
	value  string
	expiry time.Time
}

func New() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

type Relay struct {
	Hostname     string      `json:"hostname"`
	Country      string      `json:"country"`
	City         string      `json:"city"`
	IPv4         netip.Addr  `json:"ipv4"`
	IPv6         netip.Addr  `json:"ipv6"`
	PublicKey    wgtypes.Key `json:"public_key"`
	Provider     string      `json:"provider"`
	Owned        bool        `json:"owned"`
	Active       bool        `json:"active"`
	MultihopPort uint16      `json:"multihop_port"`
}

type Bridge struct {
	Hostname string     `json:"hostname"`
	Country  string     `json:"country"`
	City     string     `json:"city"`
	IPv4     netip.Addr `json:"ipv4"`
	Provider string     `json:"provider"`
	Owned    bool       `json:"owned"`
	Active   bool       `json:"active"`
}

type ShadowsocksEndpoint struct {
	Port     uint16
	Cipher   string
	Password string
}

type Device struct {
	ID        string
	Name      string
	PublicKey wgtypes.Key
	IPv4      netip.Prefix
	IPv6      netip.Prefix
	Created   time.Time
}

type APIError struct {
	StatusCode int
	Code       string
	Detail     string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("mullvad: %d %s: %s", e.StatusCode, e.Code, e.Detail)
	}
	return fmt.Sprintf("mullvad: %d %s", e.StatusCode, e.Detail)
}

func (c *Client) Relays(ctx context.Context) ([]Relay, error) {
	var raw []struct {
		Type         string `json:"type"`
		Hostname     string `json:"hostname"`
		CountryName  string `json:"country_name"`
		CityName     string `json:"city_name"`
		Active       bool   `json:"active"`
		Owned        bool   `json:"owned"`
		Provider     string `json:"provider"`
		IPv4AddrIn   string `json:"ipv4_addr_in"`
		IPv6AddrIn   string `json:"ipv6_addr_in"`
		Pubkey       string `json:"pubkey"`
		MultihopPort uint16 `json:"multihop_port"`
	}
	req, err := c.newRequest(ctx, "GET", "/www/relays/all", nil)
	if err != nil {
		return nil, err
	}
	if err := c.do(req, &raw); err != nil {
		return nil, err
	}
	out := make([]Relay, 0, len(raw))
	for _, r := range raw {
		if r.Type != "wireguard" {
			continue
		}
		key, err := wgtypes.ParseKey(r.Pubkey)
		if err != nil {
			return nil, fmt.Errorf("mullvad: relay %s: %w", r.Hostname, err)
		}
		ip4, err := netip.ParseAddr(r.IPv4AddrIn)
		if err != nil {
			return nil, fmt.Errorf("mullvad: relay %s: %w", r.Hostname, err)
		}
		var ip6 netip.Addr
		if r.IPv6AddrIn != "" {
			ip6, err = netip.ParseAddr(r.IPv6AddrIn)
			if err != nil {
				return nil, fmt.Errorf("mullvad: relay %s: %w", r.Hostname, err)
			}
		}
		out = append(out, Relay{
			Hostname:     r.Hostname,
			Country:      r.CountryName,
			City:         r.CityName,
			IPv4:         ip4,
			IPv6:         ip6,
			PublicKey:    key,
			Provider:     r.Provider,
			Owned:        r.Owned,
			Active:       r.Active,
			MultihopPort: r.MultihopPort,
		})
	}
	return out, nil
}

// Bridges fetches Shadowsocks bridges and the UDP/aes-256-gcm endpoint
// the Mullvad app uses (talpid tunnel-obfuscation/shadowsocks).
func (c *Client) Bridges(ctx context.Context) ([]Bridge, ShadowsocksEndpoint, error) {
	var raw struct {
		Locations map[string]struct {
			Country string `json:"country"`
			City    string `json:"city"`
		} `json:"locations"`
		Bridge struct {
			Relays []struct {
				Hostname   string `json:"hostname"`
				Location   string `json:"location"`
				Active     bool   `json:"active"`
				Owned      bool   `json:"owned"`
				Provider   string `json:"provider"`
				IPv4AddrIn string `json:"ipv4_addr_in"`
			} `json:"relays"`
			Shadowsocks []struct {
				Protocol string `json:"protocol"`
				Port     uint16 `json:"port"`
				Cipher   string `json:"cipher"`
				Password string `json:"password"`
			} `json:"shadowsocks"`
		} `json:"bridge"`
	}
	req, err := c.newRequest(ctx, "GET", "/app/v1/relays", nil)
	if err != nil {
		return nil, ShadowsocksEndpoint{}, err
	}
	if err := c.do(req, &raw); err != nil {
		return nil, ShadowsocksEndpoint{}, err
	}
	var ss ShadowsocksEndpoint
	for _, s := range raw.Bridge.Shadowsocks {
		if s.Protocol == "udp" && s.Cipher == "aes-256-gcm" {
			ss = ShadowsocksEndpoint{Port: s.Port, Cipher: s.Cipher, Password: s.Password}
			break
		}
	}
	if ss.Port == 0 {
		return nil, ShadowsocksEndpoint{}, errors.New("mullvad: no udp/aes-256-gcm shadowsocks endpoint")
	}
	out := make([]Bridge, 0, len(raw.Bridge.Relays))
	for _, r := range raw.Bridge.Relays {
		ip, err := netip.ParseAddr(r.IPv4AddrIn)
		if err != nil {
			return nil, ShadowsocksEndpoint{}, fmt.Errorf("mullvad: bridge %s: %w", r.Hostname, err)
		}
		loc := raw.Locations[r.Location]
		out = append(out, Bridge{
			Hostname: r.Hostname,
			Country:  loc.Country,
			City:     loc.City,
			IPv4:     ip,
			Provider: r.Provider,
			Owned:    r.Owned,
			Active:   r.Active,
		})
	}
	return out, ss, nil
}

func (c *Client) CreateAccount(ctx context.Context) (string, error) {
	req, err := c.newRequest(ctx, "POST", "/accounts/v1/accounts", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Number string `json:"number"`
	}
	if err := c.do(req, &resp); err != nil {
		return "", err
	}
	if resp.Number == "" {
		return "", errors.New("mullvad: empty account number")
	}
	return resp.Number, nil
}

func (c *Client) AccountExpiry(ctx context.Context, account string) (time.Time, error) {
	var resp struct {
		Expiry time.Time `json:"expiry"`
	}
	if err := c.authedJSON(ctx, account, "GET", "/accounts/v1/accounts/me", nil, &resp); err != nil {
		return time.Time{}, err
	}
	return resp.Expiry, nil
}

func (c *Client) ListDevices(ctx context.Context, account string) ([]Device, error) {
	var raw []deviceJSON
	if err := c.authedJSON(ctx, account, "GET", "/accounts/v1/devices", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(raw))
	for _, d := range raw {
		dev, err := d.toDevice()
		if err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, nil
}

func (c *Client) RegisterDevice(ctx context.Context, account string, pub wgtypes.Key) (Device, error) {
	body := struct {
		PubKey    string `json:"pubkey"`
		HijackDNS bool   `json:"hijack_dns"`
	}{pub.String(), false}
	var raw deviceJSON
	if err := c.authedJSON(ctx, account, "POST", "/accounts/v1/devices", body, &raw); err != nil {
		return Device{}, err
	}
	return raw.toDevice()
}

func (c *Client) RevokeDevice(ctx context.Context, account, deviceID string) error {
	return c.authedJSON(ctx, account, "DELETE", "/accounts/v1/devices/"+deviceID, nil, nil)
}

type deviceJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PubKey      string    `json:"pubkey"`
	IPv4Address string    `json:"ipv4_address"`
	IPv6Address string    `json:"ipv6_address"`
	Created     time.Time `json:"created"`
}

func (d deviceJSON) toDevice() (Device, error) {
	key, err := wgtypes.ParseKey(d.PubKey)
	if err != nil {
		return Device{}, fmt.Errorf("mullvad: device %s: %w", d.ID, err)
	}
	v4, err := netip.ParsePrefix(d.IPv4Address)
	if err != nil {
		return Device{}, fmt.Errorf("mullvad: device %s: %w", d.ID, err)
	}
	var v6 netip.Prefix
	if d.IPv6Address != "" {
		v6, err = netip.ParsePrefix(d.IPv6Address)
		if err != nil {
			return Device{}, fmt.Errorf("mullvad: device %s: %w", d.ID, err)
		}
	}
	return Device{
		ID:        d.ID,
		Name:      d.Name,
		PublicKey: key,
		IPv4:      v4,
		IPv6:      v6,
		Created:   d.Created,
	}, nil
}

func (c *Client) accessToken(ctx context.Context, account string) (string, error) {
	c.mu.Lock()
	t, ok := c.tokens[account]
	c.mu.Unlock()
	if ok && time.Until(t.expiry) > 30*time.Second {
		return t.value, nil
	}
	body, err := json.Marshal(struct {
		AccountNumber string `json:"account_number"`
	}{account})
	if err != nil {
		return "", err
	}
	req, err := c.newRequest(ctx, "POST", "/auth/v1/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var resp struct {
		AccessToken string    `json:"access_token"`
		Expiry      time.Time `json:"expiry"`
	}
	if err := c.do(req, &resp); err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.tokens == nil {
		c.tokens = make(map[string]token)
	}
	c.tokens[account] = token{value: resp.AccessToken, expiry: resp.Expiry}
	c.mu.Unlock()
	return resp.AccessToken, nil
}

func (c *Client) invalidateToken(account string) {
	c.mu.Lock()
	delete(c.tokens, account)
	c.mu.Unlock()
}

func (c *Client) authedJSON(ctx context.Context, account, method, path string, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		raw, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	send := func() error {
		tok, err := c.accessToken(ctx, account)
		if err != nil {
			return err
		}
		var body io.Reader
		if raw != nil {
			body = bytes.NewReader(raw)
		}
		req, err := c.newRequest(ctx, method, path, body)
		if err != nil {
			return err
		}
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return c.do(req, out)
	}
	err := send()
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(account)
		return send()
	}
	return err
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return http.NewRequestWithContext(ctx, method, base+path, body)
}

func (c *Client) do(req *http.Request, out any) error {
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := limitBody(resp.Body)
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(body)
		var e struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Detail == "" {
			e.Detail = string(bytes.TrimSpace(b))
		}
		return &APIError{StatusCode: resp.StatusCode, Code: e.Code, Detail: truncate(e.Detail, maxDetailBytes)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	return json.NewDecoder(body).Decode(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
