// Package netutil decides which TCP peers are trusted proxies (PE_TRUSTED_PROXIES,
// PE_TRUSTED_PROXIES_PRESET) and extracts the client IP of requests arriving through them.
package netutil

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies is the set of proxies whose forwarding headers are honoured. The zero value trusts
// nobody. Cloudflare ranges (from the preset) are tracked separately because only a Cloudflare peer
// may set CF-Connecting-IP.
type TrustedProxies struct {
	prefixes   []netip.Prefix
	cloudflare []netip.Prefix
}

// NewTrustedProxies returns a set of the given prefixes.
func NewTrustedProxies(prefixes ...netip.Prefix) TrustedProxies {
	return TrustedProxies{prefixes: normalise(prefixes)}
}

// SetCloudflare replaces the Cloudflare ranges (embedded or refreshed list).
func (t *TrustedProxies) SetCloudflare(prefixes []netip.Prefix) { t.cloudflare = normalise(prefixes) }

// Prefixes returns every trusted prefix, Cloudflare ranges included.
func (t TrustedProxies) Prefixes() []netip.Prefix {
	return append(append([]netip.Prefix(nil), t.prefixes...), t.cloudflare...)
}

// Empty reports whether no proxy is trusted.
func (t TrustedProxies) Empty() bool { return len(t.prefixes) == 0 && len(t.cloudflare) == 0 }

// Contains reports whether a is a trusted proxy address.
func (t TrustedProxies) Contains(a netip.Addr) bool {
	return t.IsCloudflare(a) || within(t.prefixes, a)
}

// IsCloudflare reports whether a is in the Cloudflare ranges of the preset.
func (t TrustedProxies) IsCloudflare(a netip.Addr) bool { return within(t.cloudflare, a) }

// ClientIP returns the client address of a request received from peer: the peer itself unless it is
// trusted; CF-Connecting-IP when the peer is a Cloudflare address; otherwise the right-most
// X-Forwarded-For hop that is not a trusted proxy (all hops trusted: the left-most one). A malformed
// hop ends the walk at the last address that could be checked.
func (t TrustedProxies) ClientIP(peer netip.Addr, h http.Header) netip.Addr {
	peer = peer.Unmap()
	if !t.Contains(peer) {
		return peer
	}
	if t.IsCloudflare(peer) {
		if a, ok := parseHop(h.Get("CF-Connecting-IP")); ok {
			return a
		}
	}
	var hops []string
	for _, v := range h.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			return client
		}
		client = a
		if !t.Contains(a) {
			return a
		}
	}
	return client
}

// PeerAddr parses an http.Request.RemoteAddr ("ip:port").
func PeerAddr(remoteAddr string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return ap.Addr().Unmap(), true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	return a.Unmap(), err == nil
}

// RequestClientIP is ClientIP for an *http.Request.
func (t TrustedProxies) RequestClientIP(r *http.Request) (netip.Addr, bool) {
	peer, ok := PeerAddr(r.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	return t.ClientIP(peer, r.Header), true
}

// PeerAllowed implements PE_REQUIRE_TRUSTED_PROXY: with require set, only trusted proxies may connect.
func (t TrustedProxies) PeerAllowed(peer netip.Addr, require bool) bool {
	return !require || t.Contains(peer.Unmap())
}

func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

func within(ps []netip.Prefix, a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func normalise(ps []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		switch {
		case !p.IsValid():
		case p.Addr().Is4In6() && p.Bits() >= 96:
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked())
		case !p.Addr().Is4In6():
			out = append(out, p.Masked())
		}
	}
	return out
}
