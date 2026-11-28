package auth

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestLockoutAfterRepeatedAttempts(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < MaxAttempts; i++ {
		if ok, _ := l.Attempt("198.51.100.7"); !ok {
			t.Fatalf("locked after only %d attempts", i)
		}
	}
	ok, wait := l.Attempt("198.51.100.7")
	if ok || wait <= 0 || wait > LockoutWindow {
		t.Fatalf("after %d attempts: allowed=%v wait=%v", MaxAttempts, ok, wait)
	}
	if ok, _ := l.Attempt("203.0.113.9"); !ok {
		t.Fatal("another address must not be locked")
	}
	clock.t = clock.t.Add(LockoutWindow + time.Second)
	if ok, _ := l.Attempt("198.51.100.7"); !ok {
		t.Fatal("lockout must end after the window")
	}
}

func TestWaitUsesOldestAttempt(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < MaxAttempts; i++ {
		l.Attempt("198.51.100.7")
		clock.t = clock.t.Add(time.Minute)
	}
	_, wait := l.Attempt("198.51.100.7")
	want := LockoutWindow - time.Duration(MaxAttempts)*time.Minute
	if wait != want {
		t.Fatalf("wait = %v, want %v (time until the oldest attempt ages out)", wait, want)
	}
}

func TestResetClearsAttempts(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < MaxAttempts-1; i++ {
		l.Attempt("198.51.100.7")
	}
	l.Reset(t.Context(), "198.51.100.7")
	for i := 0; i < MaxAttempts-1; i++ {
		if ok, _ := l.Attempt("198.51.100.7"); !ok {
			t.Fatal("a successful login must reset the attempt count")
		}
	}
}

func TestTrackedAddressesStayBounded(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < maxTracked+500; i++ {
		l.Attempt(strconv.Itoa(i))
		l.global = 0 // only the table is under test here
	}
	if n := len(l.attempts); n > maxTracked {
		t.Fatalf("tracking %d addresses, limit is %d", n, maxTracked)
	}
}

func TestFullTableEvictsOldestAddresses(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < maxTracked-1; i++ {
		l.Attempt(strconv.Itoa(i))
		clock.t = clock.t.Add(time.Millisecond)
		l.global = 0 // only the table is under test here
	}
	for i := 0; i < MaxAttempts; i++ {
		l.Attempt("victim")
	}
	l.global = 0
	l.Attempt("newcomer") // the table is full: the oldest entries go, the recent lockout stays
	if ok, _ := l.Attempt("victim"); ok {
		t.Fatal("a full table must not forget a recent lockout")
	}
	if _, ok := l.attempts["0"]; ok {
		t.Fatal("the oldest address must be evicted")
	}
	if _, ok := l.attempts["newcomer"]; !ok {
		t.Fatal("the new address must be tracked")
	}
	if n := len(l.attempts); n > maxTracked || n < maxTracked/2 {
		t.Fatalf("tracking %d addresses after eviction", n)
	}
}

func TestGlobalBudget(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < GlobalAttempts; i++ {
		if ok, _ := l.Attempt("198.51.100." + strconv.Itoa(i)); !ok {
			t.Fatalf("attempt %d refused within the budget", i)
		}
	}
	ok, wait := l.Attempt("203.0.113.9") // a fresh address: only the server-wide budget refuses it
	if ok || wait <= 0 || wait > GlobalWindow {
		t.Fatalf("over the budget: allowed=%v wait=%v", ok, wait)
	}
	for range 100 { // the flood goes on; refused attempts do not extend it
		l.Attempt("203.0.113.10")
	}
	clock.t = clock.t.Add(GlobalWindow)
	if ok, _ := l.Attempt("203.0.113.9"); !ok {
		t.Fatal("the budget must refill once the window ends")
	}
}

func TestRecentLoginBypassesGlobalBudget(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	const admin = "192.0.2.1"
	l.Attempt(admin)
	l.Reset(t.Context(), admin)
	flood(t, l)
	for i := range MaxAttempts {
		if ok, _ := l.Attempt(admin); !ok {
			t.Fatalf("attempt %d from an address that logged in recently refused during a flood", i)
		}
	}
	if ok, _ := l.Attempt(admin); ok {
		t.Fatal("the per-address lockout must still apply")
	}

	clock.t = clock.t.Add(KnownFor + GlobalWindow)
	flood(t, l)
	if ok, _ := l.Attempt(admin); ok {
		t.Fatal("a login older than KnownFor must not bypass the budget")
	}
}

// flood exhausts l's server-wide budget from addresses that never logged in.
func flood(t *testing.T, l *Limiter) {
	t.Helper()
	for i := range GlobalAttempts {
		l.Attempt("198.51.100." + strconv.Itoa(i))
	}
	if ok, _ := l.Attempt("203.0.113.9"); ok {
		t.Fatal("the flood did not exhaust the budget")
	}
}

func TestKnownAddressSurvivesRestart(t *testing.T) {
	st := newStore(t)
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	open := func() *Limiter {
		l, err := OpenLimiter(t.Context(), st, clock.now)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	const admin = "192.0.2.1"
	l := open()
	l.Attempt(admin)
	if err := l.Reset(t.Context(), admin); err != nil {
		t.Fatal(err)
	}

	clock.t = clock.t.Add(10 * 24 * time.Hour) // restarted ten days later
	l = open()
	flood(t, l)
	if ok, _ := l.Attempt(admin); !ok {
		t.Fatal("an address that logged in ten days ago, before a restart, refused during a flood")
	}

	clock.t = clock.t.Add(KnownFor - 10*24*time.Hour + time.Second)
	l = open()
	flood(t, l)
	if ok, _ := l.Attempt(admin); ok {
		t.Fatal("a login older than KnownFor must not bypass the budget after a restart")
	}
}

func TestKnownAddressesStayBounded(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := range maxKnown + 10 {
		clock.t = clock.t.Add(time.Second)
		l.Reset(t.Context(), "addr"+strconv.Itoa(i))
	}
	if len(l.known) != maxKnown {
		t.Fatalf("%d known addresses, want %d", len(l.known), maxKnown)
	}
	if _, ok := l.known["addr0"]; ok {
		t.Fatal("the oldest login was kept")
	}
	if _, ok := l.known["addr"+strconv.Itoa(maxKnown+9)]; !ok {
		t.Fatal("the newest login was evicted")
	}
}

func TestSuccessGivesBudgetBack(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := 0; i < GlobalAttempts*2; i++ {
		addr := "198.51.100." + strconv.Itoa(i)
		if ok, _ := l.Attempt(addr); !ok {
			t.Fatalf("attempt %d refused although every earlier one succeeded", i)
		}
		l.Reset(t.Context(), addr)
	}
}

func TestAttemptIsRaceFree(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]bool, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, _ := l.Attempt("198.51.100.7")
			results[i] = ok
		}(i)
	}
	wg.Wait()
	var allowed int
	for _, ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != MaxAttempts {
		t.Fatalf("allowed %d of %d concurrent attempts, want exactly %d", allowed, goroutines, MaxAttempts)
	}
}

func TestReleaseGivesTheAttemptBack(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for range 3 * MaxAttempts {
		if ok, _ := l.Attempt("198.51.100.7"); !ok {
			t.Fatal("a released attempt still counts")
		}
		l.Release("198.51.100.7")
	}
	if addrs, _ := l.Status(); len(addrs) != 0 || l.global != 0 {
		t.Fatalf("after releasing every attempt: %+v, server-wide %d", addrs, l.global)
	}
	l.Release("203.0.113.9") // nothing reserved: nothing changes
	for range MaxAttempts {
		l.Attempt("198.51.100.7")
	}
	if ok, _ := l.Attempt("198.51.100.7"); ok {
		t.Fatal("attempts that were not released must still lock out")
	}
}
