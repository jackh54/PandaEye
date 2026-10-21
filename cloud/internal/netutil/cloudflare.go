package netutil

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
)

// Cloudflare's published range lists.
const (
	CloudflareIPv4URL = "https://www.cloudflare.com/ips-v4"
	CloudflareIPv6URL = "https://www.cloudflare.com/ips-v6"
)

//go:embed cloudflare-ips.txt
var embeddedCloudflare string

// CloudflarePrefixes returns the embedded Cloudflare ranges.
func CloudflarePrefixes() []netip.Prefix {
	ps, err := parsePrefixList(strings.NewReader(embeddedCloudflare))
	if err != nil {
		panic("netutil: embedded Cloudflare list: " + err.Error())
	}
	return ps
}

// FetchCloudflare downloads the current Cloudflare ranges from the two list URLs (normally
// CloudflareIPv4URL and CloudflareIPv6URL). Callers fall back to CloudflarePrefixes on error.
func FetchCloudflare(ctx context.Context, client *http.Client, v4URL, v6URL string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, u := range []string{v4URL, v6URL} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		ps, err := func() ([]netip.Prefix, error) {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("netutil: %s: %s", u, resp.Status)
			}
			return parsePrefixList(io.LimitReader(resp.Body, 64<<10))
		}()
		if err != nil {
			return nil, err
		}
		if len(ps) == 0 {
			return nil, fmt.Errorf("netutil: %s: empty list", u)
		}
		out = append(out, ps...)
	}
	return out, nil
}

func parsePrefixList(r io.Reader) ([]netip.Prefix, error) {
	var out []netip.Prefix
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, fmt.Errorf("netutil: bad range %q", line)
		}
		if p.Bits() < 8 {
			return nil, errors.New("netutil: implausibly wide range " + line)
		}
		out = append(out, p.Masked())
	}
	return out, sc.Err()
}
