package auth

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"observarr/internal/store"
)

const (
	// MaxAttempts is how many login attempts an address may make within LockoutWindow. An attempt counts when
	// it starts, before the password is checked; only a successful login (Reset) clears the count.
	MaxAttempts = 5
	// LockoutWindow is how far back attempts count; a lockout ends when the oldest of them ages out.
	LockoutWindow = 15 * time.Minute
	// GlobalAttempts is how many unsuccessful login attempts the whole server accepts per GlobalWindow, from
	// all addresses together; beyond that attempts are refused (except as KnownFor says) until the window ends.
	// Refused attempts do not count, so logins work again at most one window after a distributed flood stops.
	GlobalAttempts = 30
	// GlobalWindow is the fixed period of the server-wide budget.
	GlobalWindow = time.Minute
	// KnownFor is how long after a successful login an address is exempt from the server-wide budget, so a
	// distributed flood cannot lock out the admin's usual address. Its own lockout still applies.
	KnownFor = 30 * 24 * time.Hour
	// maxTracked bounds memory when many addresses attempt logins (e.g. a distributed attack).
	maxTracked = 10_000
	// maxKnown bounds the addresses remembered for KnownFor; the oldest login is forgotten first.
	maxKnown = 1000
)

// Limiter slows down password guessing, per client address and server-wide.
type Limiter struct {
	mu          sync.Mutex
	now         func() time.Time
	attempts    map[string][]time.Time // recent attempt times, at most MaxAttempts per address
	known       map[string]time.Time   // time of each address's latest successful login
	windowStart time.Time              // start of the current GlobalWindow
	global      int                    // attempts in the current GlobalWindow that have not succeeded
	store       *store.Store           // keeps known across restarts; nil keeps it in memory only
}

// NewLimiter returns a Limiter that keeps everything in memory, using now as its clock.
func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, attempts: map[string][]time.Time{}, known: map[string]time.Time{}}
}

// OpenLimiter returns a Limiter whose known addresses outlive a restart: it loads the logins of the last
// KnownFor from st, and Reset writes each new one through.
func OpenLimiter(ctx context.Context, st *store.Store, now func() time.Time) (*Limiter, error) {
	known, err := st.KnownLogins(ctx, now().Add(-KnownFor))
	if err != nil {
		return nil, err
	}
	l := NewLimiter(now)
	l.known, l.store = known, st
	return l, nil
}

// Attempt reserves a login attempt from addr before the (slow) password check runs, so concurrent attempts
// can never together exceed the limits. An address that logged in within KnownFor is held to its own limit
// only. It reports whether the attempt may proceed, and if not, how long to
// wait. The caller must call Reset on a successful login.
func (l *Limiter) Attempt(addr string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.recent(addr)
	if len(recent) >= MaxAttempts {
		return false, recent[0].Add(LockoutWindow).Sub(now)
	}
	if end := l.windowStart.Add(GlobalWindow); !now.Before(end) {
		l.windowStart, l.global = now, 0
	} else if l.global >= GlobalAttempts && now.Sub(l.known[addr]) >= KnownFor {
		return false, end.Sub(now)
	}
	if recent == nil && len(l.attempts) >= maxTracked {
		l.evict()
	}
	l.attempts[addr] = append(recent, now)
	l.global++
	return true, 0
}

// Reset forgets addr's attempts, remembers its login for KnownFor (in the store too, if the Limiter has one)
// and gives one attempt back to the server-wide budget (a successful login). An error means only that the
// login could not be stored.
func (l *Limiter) Reset(ctx context.Context, addr string) error {
	at := l.reset(addr)
	if l.store == nil {
		return nil
	}
	return l.store.RememberLogin(ctx, addr, at)
}

// AddrState is what the Limiter holds about one address: its attempts within LockoutWindow (successful logins
// clear them) and, when it is locked out, for how long still.
type AddrState struct {
	Key      string        // the address key (see LimitKey)
	Attempts int           // attempts within LockoutWindow
	Last     time.Time     // the latest of them
	Locked   time.Duration // time left in its lockout; 0 if it is not locked out
}

// Status is the state of the Limiter: every address with attempts in the window, those locked out first, then by
// latest attempt; and how long the server-wide budget still refuses unknown addresses (0 if it does not).
func (l *Limiter) Status() (addrs []AddrState, globalLocked time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	addrs = make([]AddrState, 0, len(l.attempts))
	for key := range l.attempts {
		recent := l.recent(key)
		if len(recent) == 0 {
			continue
		}
		s := AddrState{Key: key, Attempts: len(recent), Last: recent[len(recent)-1]}
		if len(recent) >= MaxAttempts {
			s.Locked = max(0, recent[0].Add(LockoutWindow).Sub(now))
		}
		addrs = append(addrs, s)
	}
	slices.SortFunc(addrs, func(a, b AddrState) int {
		if c := cmp.Compare(b.Locked, a.Locked); c != 0 {
			return c
		}
		if c := b.Last.Compare(a.Last); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	if end := l.windowStart.Add(GlobalWindow); l.global >= GlobalAttempts && now.Before(end) {
		globalLocked = end.Sub(now)
	}
	return addrs, globalLocked
}

// reset does Reset's work in memory and returns the time of the login.
func (l *Limiter) reset(addr string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, addr)
	if l.global > 0 {
		l.global--
	}
	if _, ok := l.known[addr]; !ok && len(l.known) >= maxKnown {
		oldest := ""
		for a, t := range l.known {
			if oldest == "" || t.Before(l.known[oldest]) {
				oldest = a
			}
		}
		delete(l.known, oldest)
	}
	now := l.now()
	l.known[addr] = now
	return now
}

// recent returns addr's attempts inside the window and drops older ones. Caller holds mu.
func (l *Limiter) recent(addr string) []time.Time {
	cutoff := l.now().Add(-LockoutWindow)
	kept := l.attempts[addr][:0:0]
	for _, t := range l.attempts[addr] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, addr)
		return nil
	}
	l.attempts[addr] = kept
	return kept
}

// evict makes room in a full table: it drops every address whose attempts have all expired and, if that is
// not enough, the tenth of the addresses whose latest attempt is oldest, so a recent lockout survives a flood
// of new addresses. Caller holds mu.
func (l *Limiter) evict() {
	for addr := range l.attempts {
		l.recent(addr)
	}
	if len(l.attempts) < maxTracked {
		return
	}
	addrs := make([]string, 0, len(l.attempts))
	for addr := range l.attempts {
		addrs = append(addrs, addr)
	}
	latest := func(addr string) time.Time { a := l.attempts[addr]; return a[len(a)-1] }
	slices.SortFunc(addrs, func(a, b string) int { return latest(a).Compare(latest(b)) })
	for _, addr := range addrs[:maxTracked/10] {
		delete(l.attempts, addr)
	}
}
