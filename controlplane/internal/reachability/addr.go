package reachability

import "net/netip"

// blockedPrefix is one special-purpose range this service refuses to connect
// to, with the reason the refusal is reported and logged under.
type blockedPrefix struct {
	prefix netip.Prefix
	reason string
}

// blockedPrefixes are the special-purpose ranges the net/netip predicates in
// denyReason do not already cover. Everything here is either unroutable on the
// public internet or a range whose reachability from this service says nothing
// about whether a VM is reachable from the internet, so refusing costs a real
// caller nothing.
//
// The IPv6 entries that embed an IPv4 address (6to4, Teredo, NAT64,
// IPv4-compatible) are blocked wholesale rather than decoded: an attacker
// picks the embedded address, so decoding one to re-check it is a second
// parser to get wrong for a range nobody legitimately probes.
var blockedPrefixes = []blockedPrefix{
	{netip.MustParsePrefix("0.0.0.0/8"), "this-network"},
	// 169.254.0.0/16 is also link-local, which denyReason rejects before it
	// reaches this table. It is listed anyway because 169.254.169.254 is the
	// cloud metadata endpoint: this is the one range where a refactor that
	// loosens a predicate must still fail closed.
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local (cloud metadata)"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation"},
	// 240.0.0.0/4 is reserved and contains 255.255.255.255.
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
	// ::/8 covers ::1, ::, the IPv4-compatible ::a.b.c.d form, and the NAT64
	// well-known prefix 64:ff9b::/96.
	{netip.MustParsePrefix("::/8"), "IPv6 reserved"},
	{netip.MustParsePrefix("100::/64"), "IPv6 discard-only"},
	// 2001::/23 is the IETF protocol assignments block, which contains Teredo
	// (2001::/32) and ORCHIDv2.
	{netip.MustParsePrefix("2001::/23"), "IETF protocol assignments"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation"},
	{netip.MustParsePrefix("2002::/16"), "6to4"},
}

// denyReason reports why addr must not be connected to, or "" if it is a
// public destination this service may probe.
//
// The check is on a resolved address, never on the hostname the caller sent:
// the string tells you nothing, and the address is what the connection
// actually goes to. The caller must then dial this exact address rather than
// the name, or a second DNS answer can point the connection somewhere this
// function never saw.
func denyReason(addr netip.Addr) string {
	// An IPv4 address that arrived as ::ffff:169.254.169.254 is the same
	// destination as 169.254.169.254, and the IPv4 predicates and prefixes
	// below only recognise it once it is unmapped.
	addr = addr.Unmap()

	switch {
	case !addr.IsValid():
		return "not an IP address"
	case addr.IsLoopback():
		return "loopback"
	case addr.IsUnspecified():
		return "unspecified"
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		return "link-local"
	case addr.IsInterfaceLocalMulticast(), addr.IsMulticast():
		return "multicast"
	case addr.IsPrivate():
		// RFC 1918 for IPv4, and fc00::/7 unique local for IPv6.
		return "private"
	case !addr.IsGlobalUnicast():
		return "not a global unicast address"
	}

	for _, b := range blockedPrefixes {
		if b.prefix.Contains(addr) {
			return b.reason
		}
	}
	return ""
}

// selectTarget picks the address to probe, or reports why the whole request is
// refused.
//
// One blocked address in the answer refuses the request outright rather than
// being skipped over: a name that resolves to both a public address and an
// internal one is a rebinding attempt, not a host worth being helpful about.
func selectTarget(addrs []netip.Addr) (netip.Addr, string) {
	if len(addrs) == 0 {
		return netip.Addr{}, "did not resolve to any address"
	}
	for _, addr := range addrs {
		if reason := denyReason(addr); reason != "" {
			return netip.Addr{}, "resolves to a " + reason + " address"
		}
	}
	return addrs[0].Unmap(), ""
}
