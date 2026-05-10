package mullvad

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New()
	c.BaseURL = srv.URL
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func writeToken(t *testing.T, w http.ResponseWriter, value string) {
	writeJSON(t, w, map[string]any{
		"access_token": value,
		"expiry":       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
}

func TestRelays(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/www/relays/all" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Write(fixture(t, "relays.json"))
	}))
	rs, err := c.Relays(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d relays, want 2", len(rs))
	}
	se := rs[0]
	if se.Hostname != "se-sto-wg-001" || se.Country != "Sweden" || se.City != "Stockholm" {
		t.Errorf("relay[0] = %+v", se)
	}
	if !se.Active || !se.Owned || se.Provider != "31173" || se.MultihopPort != 3001 {
		t.Errorf("relay[0] flags = %+v", se)
	}
	if se.IPv4.String() != "185.213.154.66" || se.IPv6.String() != "2a03:1b20:5:f011::a01f" {
		t.Errorf("relay[0] addrs = %v %v", se.IPv4, se.IPv6)
	}
	if se.PublicKey.String() != "BLNHNoGO88LjV/wDBa7CUUwUzPq/fO2UwcGLy56hKy4=" {
		t.Errorf("relay[0] key = %s", se.PublicKey)
	}
	us := rs[1]
	if us.Hostname != "us-nyc-wg-301" || us.Active || us.Owned {
		t.Errorf("relay[1] = %+v", us)
	}
	if us.IPv6.IsValid() {
		t.Errorf("relay[1] ipv6 should be zero, got %v", us.IPv6)
	}
}

func TestBridges(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/app/v1/relays" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Write(fixture(t, "bridges.json"))
	}))
	bs, ss, err := c.Bridges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ss.Port != 1236 || ss.Cipher != "aes-256-gcm" || ss.Password != "mullvad" {
		t.Errorf("ss = %+v", ss)
	}
	if len(bs) != 2 {
		t.Fatalf("got %d bridges, want 2", len(bs))
	}
	se := bs[0]
	if se.Hostname != "se-sto-br-001" || se.Country != "Sweden" || se.City != "Stockholm" {
		t.Errorf("bridge[0] = %+v", se)
	}
	if !se.Active || !se.Owned || se.Provider != "31173" || se.IPv4.String() != "185.213.154.99" {
		t.Errorf("bridge[0] flags = %+v", se)
	}
	us := bs[1]
	if us.Hostname != "us-nyc-br-101" || us.Active || us.Owned {
		t.Errorf("bridge[1] = %+v", us)
	}
}

func TestCreateAccount(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/accounts/v1/accounts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization should be empty, got %q", got)
		}
		w.Write(fixture(t, "account_create.json"))
	}))
	num, err := c.CreateAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if num != "9876543210123456" {
		t.Errorf("number = %q", num)
	}
}

func TestCreateAccountEmptyNumber(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number":""}`))
	}))
	_, err := c.CreateAccount(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty account number") {
		t.Fatalf("err = %v, want empty account number error", err)
	}
}

func TestAccountExpiry(t *testing.T) {
	var tokenCalls atomic.Int32
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			tokenCalls.Add(1)
			var req struct {
				AccountNumber string `json:"account_number"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.AccountNumber != "1234567890123456" {
				t.Errorf("account_number = %q", req.AccountNumber)
			}
			writeToken(t, w, "mva_test_xyz")
		case "/accounts/v1/accounts/me":
			if got := r.Header.Get("Authorization"); got != "Bearer mva_test_xyz" {
				t.Errorf("Authorization = %q", got)
			}
			w.Write(fixture(t, "account_me.json"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	exp, err := c.AccountExpiry(context.Background(), "1234567890123456")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-12-01T00:00:00Z")
	if !exp.Equal(want) {
		t.Errorf("expiry = %v, want %v", exp, want)
	}
	if _, err := c.AccountExpiry(context.Background(), "1234567890123456"); err != nil {
		t.Fatal(err)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Errorf("token exchanges = %d, want 1 (cached)", got)
	}
}

func TestRegisterDevice(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			writeToken(t, w, "tok1")
		case "/accounts/v1/devices":
			if r.Method != "POST" {
				t.Errorf("method = %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			var req struct {
				PubKey    string `json:"pubkey"`
				HijackDNS bool   `json:"hijack_dns"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("body %q: %v", body, err)
			}
			if req.PubKey != "BLNHNoGO88LjV/wDBa7CUUwUzPq/fO2UwcGLy56hKy4=" {
				t.Errorf("pubkey = %q", req.PubKey)
			}
			if req.HijackDNS {
				t.Errorf("hijack_dns should be false")
			}
			w.Write(fixture(t, "device.json"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	pub, err := wgtypes.ParseKey("BLNHNoGO88LjV/wDBa7CUUwUzPq/fO2UwcGLy56hKy4=")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := c.RegisterDevice(context.Background(), "acct", pub)
	if err != nil {
		t.Fatal(err)
	}
	if dev.ID != "d-abc-123" || dev.Name != "happy-octopus" {
		t.Errorf("dev = %+v", dev)
	}
	if dev.IPv4.String() != "10.64.0.5/32" {
		t.Errorf("ipv4 = %v", dev.IPv4)
	}
	if dev.IPv6.String() != "fc00:bbbb:bbbb:bb01::4:5/128" {
		t.Errorf("ipv6 = %v", dev.IPv6)
	}
	if dev.PublicKey != pub {
		t.Errorf("pubkey roundtrip failed")
	}
}

func TestRegisterDeviceMaxDevices(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			writeToken(t, w, "tok-cap")
		case "/accounts/v1/devices":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":"MAX_DEVICES_REACHED","detail":"too many devices"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	pub, err := wgtypes.ParseKey("BLNHNoGO88LjV/wDBa7CUUwUzPq/fO2UwcGLy56hKy4=")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RegisterDevice(context.Background(), "acct", pub)
	if !errors.Is(err, ErrMaxDevices) {
		t.Fatalf("err = %v, want ErrMaxDevices", err)
	}
}

func TestListDevices(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			writeToken(t, w, "tok-list")
		case "/accounts/v1/devices":
			if r.Method != "GET" {
				t.Errorf("method = %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok-list" {
				t.Errorf("Authorization = %q", got)
			}
			w.Write(fixture(t, "devices.json"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	devs, err := c.ListDevices(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2", len(devs))
	}
	a := devs[0]
	if a.ID != "d-abc-123" || a.Name != "happy-octopus" {
		t.Errorf("dev[0] = %+v", a)
	}
	if a.IPv4.String() != "10.64.0.5/32" {
		t.Errorf("dev[0] ipv4 = %v", a.IPv4)
	}
	if a.IPv6.String() != "fc00:bbbb:bbbb:bb01::4:5/128" {
		t.Errorf("dev[0] ipv6 = %v", a.IPv6)
	}
	want, _ := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
	if !a.Created.Equal(want) {
		t.Errorf("dev[0] created = %v", a.Created)
	}
	b := devs[1]
	if b.ID != "d-def-456" || b.Name != "swift-marmot" {
		t.Errorf("dev[1] = %+v", b)
	}
	if b.IPv6.IsValid() {
		t.Errorf("dev[1] ipv6 should be zero, got %v", b.IPv6)
	}
}

func TestRevokeDevice(t *testing.T) {
	var deletes atomic.Int32
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			writeToken(t, w, "tok2")
		case "/accounts/v1/devices/d-abc-123":
			if r.Method != "DELETE" {
				t.Errorf("method = %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok2" {
				t.Errorf("Authorization = %q", got)
			}
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	if err := c.RevokeDevice(context.Background(), "acct", "d-abc-123"); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Errorf("deletes = %d", deletes.Load())
	}
}

func TestPick(t *testing.T) {
	relays := []Relay{
		{Hostname: "us-nyc-wg-001", Active: true},
		{Hostname: "us-nyc-wg-002", Active: true},
		{Hostname: "us-nyc-wg-003", Active: false},
		{Hostname: "us-lax-wg-001", Active: true},
		{Hostname: "se-sto-wg-001", Active: true},
	}
	r, err := Pick(relays, "us-nyc-wg-001")
	if err != nil || r.Hostname != "us-nyc-wg-001" {
		t.Errorf("exact: %+v, %v", r, err)
	}
	if r, err := Pick(relays, "us-nyc-wg-003"); err != nil || r.Hostname != "us-nyc-wg-003" {
		t.Errorf("exact inactive: %+v, %v", r, err)
	}
	for range 20 {
		r, err := Pick(relays, "us-nyc")
		if err != nil {
			t.Fatalf("city: %v", err)
		}
		if !strings.HasPrefix(r.Hostname, "us-nyc-") || !r.Active {
			t.Errorf("city pick = %+v", r)
		}
	}
	for range 20 {
		r, err := Pick(relays, "us")
		if err != nil {
			t.Fatalf("country: %v", err)
		}
		if !strings.HasPrefix(r.Hostname, "us-") || !r.Active {
			t.Errorf("country pick = %+v", r)
		}
	}
	if _, err := Pick(relays, ""); err == nil {
		t.Error("empty: want error")
	}
	if _, err := Pick(relays, "xx"); err == nil {
		t.Error("no country match: want error")
	}
	if _, err := Pick(relays, "us-zzz"); err == nil {
		t.Error("no city match: want error")
	}
	if _, err := Pick(relays, "us-nyc-wg-999"); err == nil {
		t.Error("missing hostname: want error")
	}
}

func TestUnauthorizedRetry(t *testing.T) {
	var tokenCalls, meCalls atomic.Int32
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			n := tokenCalls.Add(1)
			writeToken(t, w, "tok-"+strings.Repeat("x", int(n)))
		case "/accounts/v1/accounts/me":
			n := meCalls.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"code":"INVALID_ACCESS_TOKEN","detail":"stale"}`))
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok-xx" {
				t.Errorf("retry Authorization = %q", got)
			}
			w.Write(fixture(t, "account_me.json"))
		}
	}))
	if _, err := c.AccountExpiry(context.Background(), "acct"); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 {
		t.Errorf("token exchanges = %d, want 2", tokenCalls.Load())
	}
	if meCalls.Load() != 2 {
		t.Errorf("me calls = %d, want 2", meCalls.Load())
	}
}

func TestUnauthorizedRetryGivesUp(t *testing.T) {
	var meCalls atomic.Int32
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			writeToken(t, w, "tok")
		case "/accounts/v1/accounts/me":
			meCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"INVALID_ACCESS_TOKEN","detail":"nope"}`))
		}
	}))
	_, err := c.AccountExpiry(context.Background(), "acct")
	if err == nil {
		t.Fatal("want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("err = %v", err)
	}
	if meCalls.Load() != 2 {
		t.Errorf("me calls = %d, want 2", meCalls.Load())
	}
}
