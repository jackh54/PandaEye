// Command pe-healthcheck is a minimal HTTP probe for the distroless PandaEye image, which has no shell
// or curl. It exits 0 when every URL argument answers 2xx within the timeout, 1 otherwise.
//
//	pe-healthcheck http://127.0.0.1:8080/healthz
//
// Only loopback URLs are accepted: the probe runs inside the container it checks.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pe-healthcheck <http://127.0.0.1:port/path>...")
		os.Exit(2)
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, raw := range os.Args[1:] {
		if err := probe(client, raw); err != nil {
			fmt.Fprintf(os.Stderr, "pe-healthcheck: %s: %v\n", raw, err)
			os.Exit(1)
		}
	}
}

func probe(client *http.Client, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return fmt.Errorf("want an http:// URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("host must be a loopback IP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
