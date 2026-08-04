// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0.

package prober

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type ipResolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dnsCacheKey struct {
	kind     string
	network  string
	hostname string
}

func (k dnsCacheKey) singleflightKey() string {
	return k.kind + "\x00" + k.network + "\x00" + k.hostname
}

type dnsCacheEntry struct {
	ips       []net.IP
	ipAddrs   []net.IPAddr
	expiresAt time.Time
}

type cachingResolver struct {
	upstream ipResolver
	ttl      time.Duration
	now      func() time.Time

	mu          sync.RWMutex
	entries     map[dnsCacheKey]dnsCacheEntry
	nextCleanup time.Time
	group       singleflight.Group
}

func newCachingResolver(upstream ipResolver, ttl time.Duration) *cachingResolver {
	return &cachingResolver{
		upstream: upstream,
		ttl:      ttl,
		now:      time.Now,
		entries:  make(map[dnsCacheKey]dnsCacheEntry),
	}
}

func (r *cachingResolver) LookupIP(ctx context.Context, network, hostname string) ([]net.IP, error) {
	if r.ttl <= 0 || net.ParseIP(hostname) != nil {
		return r.upstream.LookupIP(ctx, network, hostname)
	}
	key := dnsCacheKey{kind: "ip", network: network, hostname: strings.ToLower(hostname)}
	entry, err := r.lookup(ctx, key, func(lookupCtx context.Context) (dnsCacheEntry, error) {
		ips, err := r.upstream.LookupIP(lookupCtx, network, hostname)
		return dnsCacheEntry{ips: cloneIPs(ips)}, err
	})
	return cloneIPs(entry.ips), err
}

func (r *cachingResolver) LookupIPAddr(ctx context.Context, hostname string) ([]net.IPAddr, error) {
	if r.ttl <= 0 || net.ParseIP(hostname) != nil {
		return r.upstream.LookupIPAddr(ctx, hostname)
	}
	key := dnsCacheKey{kind: "ipaddr", hostname: strings.ToLower(hostname)}
	entry, err := r.lookup(ctx, key, func(lookupCtx context.Context) (dnsCacheEntry, error) {
		addrs, err := r.upstream.LookupIPAddr(lookupCtx, hostname)
		return dnsCacheEntry{ipAddrs: cloneIPAddrs(addrs)}, err
	})
	return cloneIPAddrs(entry.ipAddrs), err
}

func (r *cachingResolver) lookup(ctx context.Context, key dnsCacheKey, resolve func(context.Context) (dnsCacheEntry, error)) (dnsCacheEntry, error) {
	if entry, ok := r.cached(key); ok {
		return entry, nil
	}
	if err := ctx.Err(); err != nil {
		return dnsCacheEntry{}, err
	}

	result := r.group.DoChan(key.singleflightKey(), func() (any, error) {
		if entry, ok := r.cached(key); ok {
			return entry, nil
		}
		// The system resolver owns DNS transport timeouts. Probe cancellation is
		// handled per waiter below and must not cancel work needed by other probes.
		entry, err := resolve(context.Background())
		if err != nil {
			return dnsCacheEntry{}, err
		}
		r.store(key, entry)
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return dnsCacheEntry{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return dnsCacheEntry{}, result.Err
		}
		return result.Val.(dnsCacheEntry), nil
	}
}

func (r *cachingResolver) cached(key dnsCacheKey) (dnsCacheEntry, bool) {
	r.mu.RLock()
	entry, ok := r.entries[key]
	r.mu.RUnlock()
	if !ok || !r.now().Before(entry.expiresAt) {
		return dnsCacheEntry{}, false
	}
	return entry, true
}

func (r *cachingResolver) store(key dnsCacheKey, entry dnsCacheEntry) {
	now := r.now()
	entry.expiresAt = now.Add(r.ttl)
	r.mu.Lock()
	if r.nextCleanup.IsZero() || !now.Before(r.nextCleanup) {
		for cachedKey, cachedEntry := range r.entries {
			if !now.Before(cachedEntry.expiresAt) {
				delete(r.entries, cachedKey)
			}
		}
		r.nextCleanup = now.Add(r.ttl)
	}
	r.entries[key] = entry
	r.mu.Unlock()
}

func cloneIPs(ips []net.IP) []net.IP {
	cloned := make([]net.IP, len(ips))
	for i, ip := range ips {
		cloned[i] = append(net.IP(nil), ip...)
	}
	return cloned
}

func cloneIPAddrs(addrs []net.IPAddr) []net.IPAddr {
	cloned := make([]net.IPAddr, len(addrs))
	for i, addr := range addrs {
		cloned[i] = net.IPAddr{IP: append(net.IP(nil), addr.IP...), Zone: addr.Zone}
	}
	return cloned
}
