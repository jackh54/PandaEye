package discord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ErrRefusedAddress is returned when a webhook host resolves only to addresses PandaEye must not
// connect to (loopback, private, link-local, CGNAT, metadata, reserved).
var ErrRefusedAddress = errors.New("discord: refused non-public address")

// refusedPrefixes are special-purpose ranges beyond what netip's predicates cover (RFC 6890 and
// successors): "this network", CGNAT (also Alibaba's metadata 100.100.100.200), IETF protocol
// assignments, documentation, benchmarking, reserved/broadcast, NAT64, documentation v6, ORCHID,
// discard-only. Ranges that embed an IPv4 address (6to4, NAT64) are checked on the embedded address.
var refusedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "3fff::/20", "5f00::/16",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4  = netip.MustParsePrefix("2002::/16")
	teredo  = netip.MustParsePrefix("2001::/32")
	metaV6  = netip.MustParseAddr("fd00:ec2::254")
	metaV4  = netip.MustParseAddr("169.254.169.254")
	metaAli = netip.MustParseAddr("100.100.100.200")
)

// PublicAddr reports whether a is a public unicast address a webhook may be delivered to.
func PublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap().WithZone("")
	switch {
	case a == metaV4, a == metaV6, a == metaAli:
		return false
	case a.IsLoopback(), a.IsPrivate(), a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(),
		a.IsInterfaceLocalMulticast(), a.IsMulticast(), a.IsUnspecified():
		return false
	}
	for _, p := range refusedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64.Contains(a):
			return PublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixTo4.Contains(a):
			return PublicAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		case teredo.Contains(a):
			return false
		}
	}
	return true
}

// resolver looks up the addresses of a host (net.DefaultResolver; tests inject their own).
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// guardedDialer resolves webhook hosts itself, keeps only public addresses and dials those
// addresses directly, so a DNS answer can never point the client at an internal service. The
// dialer's Control re-checks the address of every socket as a second line of defence.
type guardedDialer struct {
	resolve resolver
	dialer  *net.Dialer
	// dialAddr dials an already checked address (tests redirect it to a local server).
	dialAddr func(ctx context.Context, network string, addr netip.AddrPort) (net.Conn, error)
}

func newGuardedDialer(r resolver) *guardedDialer {
	if r == nil {
		r = net.DefaultResolver
	}
	g := &guardedDialer{resolve: r}
	g.dialer = &net.Dialer{Timeout: requestTimeout, Control: controlPublicOnly}
	g.dialAddr = func(ctx context.Context, network string, addr netip.AddrPort) (net.Conn, error) {
		return g.dialer.DialContext(ctx, network, addr.String())
	}
	return g
}

// controlPublicOnly refuses sockets to non-public addresses.
func controlPublicOnly(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !PublicAddr(ap.Addr()) {
		return ErrRefusedAddress
	}
	return nil
}

func (g *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrRefusedAddress
	}
	if !webhookHosts[host] || portStr != "443" {
		return nil, ErrRefusedAddress
	}
	addrs, err := g.resolve.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("discord: resolve %s: %w", host, unwrapDNS(err))
	}
	var lastErr error = ErrRefusedAddress
	for _, a := range addrs {
		if !PublicAddr(a) {
			continue
		}
		conn, err := g.dialAddr(ctx, network, netip.AddrPortFrom(a.Unmap(), 443))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// unwrapDNS keeps DNS errors short (they name the host, never a URL).
func unwrapDNS(err error) error {
	var de *net.DNSError
	if errors.As(err, &de) {
		return errors.New(de.Err)
	}
	return err
}
