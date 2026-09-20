package netpolicy

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const maxPolicyFileBytes = 1024 * 1024

type addressRange struct {
	first netip.Addr
	last  netip.Addr
}

// Policy is an immutable set of individual addresses, prefixes, and optional ranges.
type Policy struct {
	prefixes []netip.Prefix
	ranges   []addressRange
}

func Parse(entries []string, filePath string, allowRanges bool) (*Policy, error) {
	allEntries := append([]string(nil), entries...)
	if filePath != "" {
		fileEntries, err := readEntries(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filePath, err)
		}
		allEntries = append(allEntries, fileEntries...)
	}

	policy := &Policy{}
	for _, raw := range allEntries {
		for _, part := range strings.Split(raw, ",") {
			entry := strings.TrimSpace(part)
			if entry == "" {
				return nil, errors.New("IP policy contains an empty entry")
			}
			if err := policy.add(entry, allowRanges); err != nil {
				return nil, fmt.Errorf("invalid IP policy entry %q: %w", entry, err)
			}
		}
	}
	return policy, nil
}

func readEntries(filePath string) ([]string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxPolicyFileBytes {
		return nil, fmt.Errorf("file exceeds the %d-byte limit", maxPolicyFileBytes)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := []string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxPolicyFileBytes)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "#") {
			return nil, fmt.Errorf("line %d: inline comments are not supported", lineNumber)
		}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (p *Policy) add(entry string, allowRanges bool) error {
	if strings.Contains(entry, "-") {
		if !allowRanges {
			return errors.New("ranges are not allowed here; use an IP address or CIDR prefix")
		}
		first, last, err := parseRange(entry)
		if err != nil {
			return err
		}
		candidate := addressRange{first: first, last: last}
		for _, prefix := range p.prefixes {
			if prefix.Contains(candidate.first) && prefix.Contains(candidate.last) {
				return nil
			}
		}
		merged := candidate
		remaining := p.ranges[:0]
		for _, existing := range p.ranges {
			if rangesTouch(merged, existing) {
				if existing.first.Compare(merged.first) < 0 {
					merged.first = existing.first
				}
				if existing.last.Compare(merged.last) > 0 {
					merged.last = existing.last
				}
				continue
			}
			remaining = append(remaining, existing)
		}
		p.ranges = append(remaining, merged)
		return nil
	}
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return err
		}
		if prefix.Addr().Is4In6() && prefix.Bits() < 96 {
			return errors.New("IPv4-mapped IPv6 prefixes must be at least /96")
		}
		p.addPrefix(normalizePrefix(prefix))
		return nil
	}
	address, err := netip.ParseAddr(entry)
	if err != nil {
		return err
	}
	address = address.Unmap()
	p.addPrefix(netip.PrefixFrom(address, address.BitLen()))
	return nil
}

func (p *Policy) addPrefix(prefix netip.Prefix) {
	for _, existing := range p.prefixes {
		if existing.Bits() <= prefix.Bits() && existing.Contains(prefix.Addr()) {
			return
		}
	}
	filteredPrefixes := p.prefixes[:0]
	for _, existing := range p.prefixes {
		if prefix.Bits() <= existing.Bits() && prefix.Contains(existing.Addr()) {
			continue
		}
		filteredPrefixes = append(filteredPrefixes, existing)
	}
	p.prefixes = filteredPrefixes
	for _, existing := range p.prefixes {
		if existing == prefix {
			return
		}
	}
	p.prefixes = append(p.prefixes, prefix)
	filteredRanges := p.ranges[:0]
	for _, interval := range p.ranges {
		if prefix.Contains(interval.first) && prefix.Contains(interval.last) {
			continue
		}
		filteredRanges = append(filteredRanges, interval)
	}
	p.ranges = filteredRanges
}

func parseRange(entry string) (netip.Addr, netip.Addr, error) {
	parts := strings.Split(entry, "-")
	if len(parts) != 2 {
		return netip.Addr{}, netip.Addr{}, errors.New("range must contain exactly one '-' separator")
	}
	first, err := netip.ParseAddr(strings.TrimSpace(parts[0]))
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid range start: %w", err)
	}
	first = first.Unmap()

	rawLast := strings.TrimSpace(parts[1])
	last, err := netip.ParseAddr(rawLast)
	if err != nil && first.Is4() {
		last, err = parseShortIPv4RangeEnd(first, rawLast)
	}
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid range end: %w", err)
	}
	last = last.Unmap()
	if first.BitLen() != last.BitLen() {
		return netip.Addr{}, netip.Addr{}, errors.New("range endpoints must use the same address family")
	}
	if first.Compare(last) > 0 {
		return netip.Addr{}, netip.Addr{}, errors.New("range end precedes its start")
	}
	return first, last, nil
}

func parseShortIPv4RangeEnd(first netip.Addr, value string) (netip.Addr, error) {
	lastOctet, err := strconv.ParseUint(value, 10, 8)
	if err != nil {
		return netip.Addr{}, errors.New("short range end must be an IPv4 octet from 0 to 255")
	}
	bytes := first.As4()
	bytes[3] = byte(lastOctet)
	return netip.AddrFrom4(bytes), nil
}

func normalizePrefix(prefix netip.Prefix) netip.Prefix {
	address := prefix.Addr().Unmap()
	bits := prefix.Bits()
	if prefix.Addr().Is4In6() {
		bits -= 96
	}
	return netip.PrefixFrom(address, bits).Masked()
}

func rangesTouch(left, right addressRange) bool {
	if left.first.BitLen() != right.first.BitLen() {
		return false
	}
	if left.first.Compare(right.last) <= 0 && right.first.Compare(left.last) <= 0 {
		return true
	}
	if left.last.Compare(right.first) < 0 {
		next := left.last.Next()
		return next.IsValid() && next.Compare(right.first) >= 0
	}
	if right.last.Compare(left.first) < 0 {
		next := right.last.Next()
		return next.IsValid() && next.Compare(left.first) >= 0
	}
	return false
}

func (p *Policy) Enabled() bool {
	return p != nil && (len(p.prefixes) > 0 || len(p.ranges) > 0)
}

func (p *Policy) Contains(address netip.Addr) bool {
	if p == nil || !address.IsValid() {
		return false
	}
	address = address.Unmap()
	for _, prefix := range p.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	for _, interval := range p.ranges {
		if interval.first.BitLen() == address.BitLen() && interval.first.Compare(address) <= 0 && address.Compare(interval.last) <= 0 {
			return true
		}
	}
	return false
}
