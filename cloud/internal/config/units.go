package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseSize parses byte sizes: plain bytes, KB/MB/GB/TB (powers of 1000) or KiB/MiB/GiB/TiB (powers
// of 1024), case-insensitive, optional space ("10GiB", "512 MiB").
func ParseSize(v string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	units := []struct {
		suffix string
		mult   float64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12}, {"b", 1},
	}
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.mult
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || f*mult > math.MaxInt64 {
		return 0, fmt.Errorf("not a size: %q", v)
	}
	return int64(f * mult), nil
}

// ParseDuration parses Go durations plus a day suffix ("7d", "1d12h").
func ParseDuration(v string) (time.Duration, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return 0, fmt.Errorf("not a duration: %q", v)
	}
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i > 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("not a duration: %q", v)
		}
		days, s = time.Duration(n)*24*time.Hour, s[i+1:]
	}
	if s == "" {
		return days, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("not a duration: %q", v)
	}
	return days + d, nil
}
