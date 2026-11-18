package provider

import (
	"context"
	"fmt"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"net"
	"net/http"
	"time"
)

func FromConfig(pc config.ProviderConfig) Provider {
	p := NewProvider(pc.APIKey, pc.APIBase, pc.APIType)
	if !pc.BYOK {
		return p
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = publicDial
	c := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	switch v := p.(type) {
	case *OpenAIProvider:
		v.client = c
	case *AnthropicProvider:
		v.client = c
	}
	return p
}
func publicDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("byok: no addresses for %s", host)
	}
	for _, ip := range ips {
		if blockedAddr(ip.IP) {
			return nil, fmt.Errorf("byok: blocked address %s for %s", ip.IP, host)
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// blockedAddr: loopback, private, link-local, multicast, unspecified and
// CGNAT addresses (same list as web_fetch's and connectors' guards).
func blockedAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 || (ip4[0] == 100 && ip4[1]&0xc0 == 0x40) {
			return true
		}
	}
	return false
}
