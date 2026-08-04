// Copyright 2026 The Prometheus Authors
// Licensed under the Apache License, Version 2.0.

package prober

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type countingResolver struct {
	mu                sync.Mutex
	lookupIPCalls     int
	lookupIPAddrCalls int
	lookupStarted     chan struct{}
	releaseLookup     chan struct{}
	err               error
}

func (r *countingResolver) LookupIP(_ context.Context, network, _ string) ([]net.IP, error) {
	r.mu.Lock()
	r.lookupIPCalls++
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	if network == "ip6" {
		return []net.IP{net.ParseIP("2001:db8::1")}, nil
	}
	return []net.IP{net.ParseIP("192.0.2.10")}, nil
}

func (r *countingResolver) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	r.mu.Lock()
	r.lookupIPAddrCalls++
	firstCall := r.lookupIPAddrCalls == 1
	r.mu.Unlock()
	if firstCall && r.lookupStarted != nil {
		close(r.lookupStarted)
	}
	if r.releaseLookup != nil {
		select {
		case <-r.releaseLookup:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
}

func (r *countingResolver) calls() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupIPCalls, r.lookupIPAddrCalls
}

func TestCachingResolverCachesSuccessfulLookupsUntilExpiry(t *testing.T) {
	upstream := &countingResolver{}
	now := time.Unix(1_000, 0)
	resolver := newCachingResolver(upstream, time.Minute)
	resolver.now = func() time.Time { return now }

	first, err := resolver.LookupIPAddr(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("first lookup failed: %v", err)
	}
	second, err := resolver.LookupIPAddr(context.Background(), "EXAMPLE.com")
	if err != nil {
		t.Fatalf("cached lookup failed: %v", err)
	}
	if _, calls := upstream.calls(); calls != 1 {
		t.Fatalf("upstream calls before expiry = %d, want 1", calls)
	}
	if first[0].String() != second[0].String() {
		t.Fatalf("cached address = %s, want %s", second[0].String(), first[0].String())
	}

	now = now.Add(time.Minute)
	if _, err := resolver.LookupIPAddr(context.Background(), "example.com"); err != nil {
		t.Fatalf("lookup after expiry failed: %v", err)
	}
	if _, calls := upstream.calls(); calls != 2 {
		t.Fatalf("upstream calls after expiry = %d, want 2", calls)
	}
}

func TestCachingResolverCoalescesConcurrentLookups(t *testing.T) {
	upstream := &countingResolver{
		lookupStarted: make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
	resolver := newCachingResolver(upstream, time.Minute)
	const workers = 50
	start := make(chan struct{})
	results := make(chan error, workers)

	for range workers {
		go func() {
			<-start
			_, err := resolver.LookupIPAddr(context.Background(), "example.com")
			results <- err
		}()
	}
	close(start)
	<-upstream.lookupStarted
	close(upstream.releaseLookup)

	for range workers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent lookup failed: %v", err)
		}
	}
	if _, calls := upstream.calls(); calls != 1 {
		t.Fatalf("concurrent upstream calls = %d, want 1", calls)
	}
}

func TestCachingResolverLetsCanceledWaiterReturn(t *testing.T) {
	upstream := &countingResolver{
		lookupStarted: make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
	resolver := newCachingResolver(upstream, time.Minute)
	firstResult := make(chan error, 1)
	go func() {
		_, err := resolver.LookupIPAddr(context.Background(), "example.com")
		firstResult <- err
	}()
	<-upstream.lookupStarted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledResult := make(chan error, 1)
	go func() {
		_, err := resolver.LookupIPAddr(ctx, "example.com")
		canceledResult <- err
	}()

	select {
	case err := <-canceledResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(upstream.releaseLookup)
		<-firstResult
		<-canceledResult
		t.Fatal("canceled waiter remained blocked on the shared lookup")
	}

	close(upstream.releaseLookup)
	if err := <-firstResult; err != nil {
		t.Fatalf("shared lookup failed: %v", err)
	}
	if _, calls := upstream.calls(); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
}

func TestCachingResolverLeaderCancellationDoesNotFailWaiter(t *testing.T) {
	upstream := &countingResolver{
		lookupStarted: make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
	resolver := newCachingResolver(upstream, time.Minute)
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := resolver.LookupIPAddr(leaderCtx, "example.com")
		leaderResult <- err
	}()
	<-upstream.lookupStarted

	waiterResult := make(chan error, 1)
	waiterStarted := make(chan struct{})
	go func() {
		close(waiterStarted)
		_, err := resolver.LookupIPAddr(context.Background(), "example.com")
		waiterResult <- err
	}()
	<-waiterStarted
	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}

	close(upstream.releaseLookup)
	if err := <-waiterResult; err != nil {
		t.Fatalf("waiter failed after leader cancellation: %v", err)
	}
	if _, calls := upstream.calls(); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
}

func TestCachingResolverDoesNotCacheFailures(t *testing.T) {
	upstream := &countingResolver{err: errors.New("dns unavailable")}
	resolver := newCachingResolver(upstream, time.Minute)

	for range 2 {
		if _, err := resolver.LookupIPAddr(context.Background(), "example.com"); err == nil {
			t.Fatal("lookup succeeded, want error")
		}
	}
	if _, calls := upstream.calls(); calls != 2 {
		t.Fatalf("failed upstream calls = %d, want 2", calls)
	}
}

func TestCachingResolverSeparatesLookupMethodsAndProtocols(t *testing.T) {
	upstream := &countingResolver{}
	resolver := newCachingResolver(upstream, time.Minute)
	ctx := context.Background()

	if _, err := resolver.LookupIP(ctx, "ip4", "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LookupIP(ctx, "ip6", "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LookupIPAddr(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if ipCalls, addrCalls := upstream.calls(); ipCalls != 2 || addrCalls != 1 {
		t.Fatalf("upstream calls = LookupIP:%d LookupIPAddr:%d, want 2 and 1", ipCalls, addrCalls)
	}
}
