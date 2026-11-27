package tools

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
)

// checkHost resolves a fetch target and refuses it when any address is blocked.
// An allowed origin relaxes only the loopback/private set.
func (f *builtinFetcher) checkHost(ctx context.Context, target *url.URL, allowed bool) error {
	host := target.Hostname()
	addrs, err := f.resolve(ctx, host)
	if err != nil {
		return webErrorf(CodeFetchFailed, "web_fetch: resolve %s: %v", host, err)
	}
	if refused, configurable := f.classifyBlocked(addrs, allowed); refused {
		return webErrorf(CodeBlockedAddress, "%s", blockedAddressMessage(host, configurable))
	}
	return nil
}

// dialContext resolves, checks every address, and dials a checked address so a
// rebinding resolver cannot swap in a blocked destination. The request context
// carries whether its origin is allowed.
func (f *builtinFetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := f.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if refused, configurable := f.classifyBlocked(addrs, webAllowedOrigin(ctx)); refused {
		return nil, &blockedAddressError{host: host, configurable: configurable}
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := f.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s has no usable address", host)
	}
	return nil, lastErr
}

// resolve returns the literal address or the resolver's answer.
func (f *builtinFetcher) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	return addrs, nil
}

// classifyBlocked reports whether any resolved address is refused without an
// allowed origin, or, for an allowed origin, whether any is always refused. The
// configurable result is true only when every refused address is a
// loopback/private one, so the error can name the allowlist safely.
func (f *builtinFetcher) classifyBlocked(addrs []netip.Addr, allowed bool) (refused, configurable bool) {
	configurable = true
	for _, addr := range addrs {
		if !f.blocked(addr, allowed) {
			continue
		}
		refused = true
		if isAlwaysBlockedWebAddr(addr) {
			configurable = false
		}
	}
	return refused, refused && configurable
}

// allowedOrigin reports whether a fetch target's origin is in the operator
// allowlist.
func (f *builtinFetcher) allowedOrigin(u *url.URL) bool {
	if len(f.allowedOrigins) == 0 {
		return false
	}
	return f.allowedOrigins[webOrigin(u)]
}

// webAllowedOriginKey carries one request's origin decision into the dialer.
type webAllowedOriginKey struct{}

func withWebAllowedOrigin(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, webAllowedOriginKey{}, allowed)
}

func webAllowedOrigin(ctx context.Context) bool {
	allowed, _ := ctx.Value(webAllowedOriginKey{}).(bool)
	return allowed
}

// blocked reports whether an address is outside the allowed public unicast set
// for this request. The test-only loopback seam allows loopback literals and
// nothing else.
func (f *builtinFetcher) blocked(addr netip.Addr, allowed bool) bool {
	if f.allowLoopback {
		if unmap := addr.Unmap(); unmap.IsLoopback() {
			return false
		}
	}
	if isAlwaysBlockedWebAddr(addr) {
		return true
	}
	return !allowed && isConfigurableBlockedWebAddr(addr)
}

// blockedAddressError reports a hostname whose resolution is refused. It stays
// typed so the tool can return blocked_address instead of fetch_failed.
type blockedAddressError struct {
	host string
	// configurable reports that only loopback/private addresses refused the
	// host, so the message can name the allowlist.
	configurable bool
}

// Error implements error.
func (e *blockedAddressError) Error() string { return e.message() }

func (e *blockedAddressError) message() string {
	return blockedAddressMessage(e.host, e.configurable)
}

// blockedAddressMessage is the model-visible refusal. The allowlist hint is
// configuration guidance for the operator reading the transcript; the model
// cannot change it.
func blockedAddressMessage(host string, configurable bool) string {
	if configurable {
		return fmt.Sprintf("web_fetch: %s resolves to a loopback or private address; add its origin to the host's web fetch allowed origins to permit it", host)
	}
	return fmt.Sprintf("web_fetch: %s resolves to a blocked address", host)
}

// alwaysBlockedWebPrefixes are refused even for an allowed origin: unspecified,
// link-local, metadata, multicast, documentation, benchmarking, reserved, and
// protocol-assignment ranges.
var alwaysBlockedWebPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// configurableBlockedWebPrefixes are refused by default and permitted for an
// allowed origin: private and shared address space. Loopback is handled by
// IsLoopback so both address families share one rule.
var configurableBlockedWebPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// nat64Prefix embeds an IPv4 destination in an IPv6 address.
var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// isAlwaysBlockedWebAddr reports whether addr is refused even for an allowed
// origin. IPv4-mapped and NAT64 addresses are checked through the embedded
// IPv4 address, so a wrapped destination keeps its classification.
func isAlwaysBlockedWebAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.Is6() && nat64Prefix.Contains(addr) {
		embedded := addr.As16()
		return isAlwaysBlockedWebAddr(netip.AddrFrom4([4]byte{embedded[12], embedded[13], embedded[14], embedded[15]}))
	}
	if addr.IsLoopback() {
		return false
	}
	if !addr.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range alwaysBlockedWebPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// isConfigurableBlockedWebAddr reports whether addr is one an allowed origin may
// reach: loopback, private, or shared address space.
func isConfigurableBlockedWebAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.Is6() && nat64Prefix.Contains(addr) {
		embedded := addr.As16()
		return isConfigurableBlockedWebAddr(netip.AddrFrom4([4]byte{embedded[12], embedded[13], embedded[14], embedded[15]}))
	}
	if addr.IsLoopback() {
		return true
	}
	for _, prefix := range configurableBlockedWebPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// isBlockedWebAddr reports whether addr is refused without an allowed origin.
func isBlockedWebAddr(addr netip.Addr) bool {
	return isAlwaysBlockedWebAddr(addr) || isConfigurableBlockedWebAddr(addr)
}
