package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment switches.
const (
	// EnvAllowPrivate lets a hook URL be http and/or resolve to a private,
	// loopback or link-local address (local development only).
	EnvAllowPrivate = "FASTCLAW_BILLING_HOOK_ALLOW_PRIVATE"
	// EnvFailClosed denies the work when the hook can't be reached or
	// answers anything but a valid decision (default: allow).
	EnvFailClosed = "FASTCLAW_BILLING_HOOK_FAIL_CLOSED"
)

// HookTimeout bounds one authorize call.
const HookTimeout = 5 * time.Second

// MinSecretLen is the shortest hook secret accepted at registration.
const MinSecretLen = 32

// SignatureHeader carries t=<unix seconds>,v1=<hex hmac-sha256(secret, t + "." + body)>.
const SignatureHeader = "X-Fastclaw-Signature"

// Messages shown when FastClaw itself refuses (the hook gave no message,
// or fail-closed after a hook failure).
const (
	defaultDenyMessage = "Sorry, this request was not authorized by your service provider's billing system."
	failClosedMessage  = "Sorry, billing is temporarily unavailable, so this request can't run right now. Please try again later."
)

func allowPrivate() bool { return envTrue(EnvAllowPrivate) }
func failClosed() bool   { return envTrue(EnvFailClosed) }

func envTrue(name string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return v
}

// Estimate is a tool call's expected size, sent with tool-stage requests.
type Estimate struct {
	Units float64 `json:"units"`
	Unit  string  `json:"unit"`
}

// AuthorizeRequest is the body FastClaw POSTs to a billing hook.
type AuthorizeRequest struct {
	ID         string    `json:"id"`
	Stage      string    `json:"stage"`
	Kind       string    `json:"kind"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Estimate   *Estimate `json:"estimate,omitempty"`
	EndUser    string    `json:"end_user"`
	AgentID    string    `json:"agent_id"`
	SessionKey string    `json:"session_key"`
	Channel    string    `json:"channel"`
	TurnID     string    `json:"turn_id"`
}

// Denied is the error a costly tool returns when the hook refused it; its
// message goes to the model as the tool's error.
type Denied struct{ Message string }

func (d *Denied) Error() string { return d.Message }

// AuthorizeTurn asks the paying account's hook whether this turn may run.
// "" means allowed; otherwise the message to show the user.
func (t *Turn) AuthorizeTurn(ctx context.Context, provider, model string) string {
	return t.authorize(ctx, AuthorizeRequest{Stage: StageTurn, Kind: KindTurn, Provider: provider, Model: model})
}

// AuthorizeTool asks the hook of the turn in ctx before a costly tool call
// (kind image / tts / sandbox). nil means go ahead; a *Denied carries the
// message for the model. No turn or no hook → nil.
func AuthorizeTool(ctx context.Context, kind, provider, model string, est Estimate) error {
	t := TurnFrom(ctx)
	if t == nil {
		return nil
	}
	if msg := t.authorize(ctx, AuthorizeRequest{Stage: StageTool, Kind: kind, Provider: provider, Model: model, Estimate: &est}); msg != "" {
		return &Denied{Message: msg}
	}
	return nil
}

func (t *Turn) authorize(ctx context.Context, req AuthorizeRequest) string {
	if t == nil || t.svc == nil || t.svc.st == nil {
		return ""
	}
	target, ok := t.billingTarget(ctx)
	if !ok || target.HookURL == "" {
		return ""
	}
	req.ID = NewID("authz_")
	req.EndUser = target.EndUser
	req.AgentID = t.AgentID
	req.SessionKey = t.SessionKey
	req.Channel = t.Channel
	req.TurnID = t.ID
	allow, msg, err := t.svc.hook.call(ctx, target.HookURL, target.HookSecret, req)
	if err != nil {
		if failClosed() {
			slog.Warn("billing hook failed; denying (fail closed)", "account", target.AccountID, "stage", req.Stage, "kind", req.Kind, "error", err)
			return failClosedMessage
		}
		slog.Warn("billing hook failed; allowing", "account", target.AccountID, "stage", req.Stage, "kind", req.Kind, "error", err)
		return ""
	}
	if allow {
		return ""
	}
	if strings.TrimSpace(msg) == "" {
		msg = defaultDenyMessage
	}
	return msg
}

// Sign returns the X-Fastclaw-Signature value for body at time ts.
func Sign(secret string, ts int64, body []byte) string {
	t := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

type hookClient struct {
	http *http.Client
	now  func() time.Time
}

func newHookClient() *hookClient {
	return &hookClient{
		now: time.Now,
		http: &http.Client{
			Timeout: HookTimeout,
			Transport: &http.Transport{
				DialContext:           guardedDial,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   HookTimeout,
				ResponseHeaderTimeout: HookTimeout,
				IdleConnTimeout:       60 * time.Second,
				MaxIdleConnsPerHost:   4,
			},
			// A redirect is not a decision.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *hookClient) call(ctx context.Context, hookURL, secret string, req AuthorizeRequest) (allow bool, message string, err error) {
	if err := checkScheme(hookURL); err != nil {
		return false, "", err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return false, "", err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), HookTimeout)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, hookURL, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("User-Agent", "FastClaw-Billing/1")
	hr.Header.Set(SignatureHeader, Sign(secret, c.now().Unix(), body))
	resp, err := c.http.Do(hr)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("hook answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		Allow   *bool  `json:"allow"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(blob, &out); err != nil || out.Allow == nil {
		return false, "", errors.New(`hook answer is not {"allow": true|false}`)
	}
	return *out.Allow, out.Message, nil
}

func checkScheme(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme == "https" || (u.Scheme == "http" && allowPrivate()) {
		return nil
	}
	return fmt.Errorf("hook url must be https")
}

// ValidateHookURL checks a hook URL at registration: absolute https (http
// too with EnvAllowPrivate), no credentials, and every address it resolves
// to public unless EnvAllowPrivate. The address is checked again at dial
// time, so a later DNS change can't point the hook inside.
func ValidateHookURL(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("url must be an absolute https URL")
	}
	if err := checkScheme(u.String()); err != nil {
		return errors.New("url must use https")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	if allowPrivate() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, HookTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("url host %q does not resolve", u.Hostname())
	}
	for _, ip := range ips {
		if isBlockedAddr(ip.IP) {
			return fmt.Errorf("url host %q resolves to a non-public address", u.Hostname())
		}
	}
	return nil
}

// guardedDial resolves the host, refuses non-public addresses (unless
// EnvAllowPrivate) and dials the address it checked, so DNS can't swap in
// another between check and connect.
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	if !allowPrivate() {
		for _, ip := range ips {
			if isBlockedAddr(ip.IP) {
				return nil, fmt.Errorf("billing hook: blocked address %s for host %s", ip.IP, host)
			}
		}
	}
	d := &net.Dialer{Timeout: HookTimeout, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// isBlockedAddr mirrors web_fetch's SSRF guard: loopback, private,
// link-local, multicast, unspecified and CGNAT addresses.
func isBlockedAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1]&0xc0 == 0x40 { // 100.64.0.0/10 CGNAT
			return true
		}
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		if ip4[0] == 0 {
			return true
		}
	}
	return false
}
