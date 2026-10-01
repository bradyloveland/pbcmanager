package auth

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Throttle slows down guessing, as in 1.x: every 5th failure from one address
// locks that address out for a minute, every 20th failure overall pauses all
// sign-ins for 5 minutes, and each failure costs Delay.
type Throttle struct {
	Delay time.Duration
	Now   func() time.Time

	mu        sync.Mutex
	perIP     map[string]*ipFailures
	acctCount int
	acctUntil time.Time
}

type ipFailures struct {
	count int
	until time.Time
	last  time.Time
}

// ThrottledError tells the user how long to wait.
type ThrottledError struct{ Wait time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("Too many attempts. Try again in %d seconds.", int(e.Wait.Seconds())+1)
}

// NewThrottle returns a throttle with a one-second delay per failure.
func NewThrottle() *Throttle {
	return &Throttle{Delay: time.Second, Now: time.Now, perIP: map[string]*ipFailures{}}
}

// Check returns a *ThrottledError if ip must wait before trying again.
func (t *Throttle) Check(ip string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	until := t.acctUntil
	if f := t.perIP[ip]; f != nil && f.until.After(until) {
		until = f.until
	}
	if wait := until.Sub(t.Now()); wait > 0 {
		return &ThrottledError{Wait: wait}
	}
	return nil
}

// Fail records a failed attempt and then sleeps for Delay.
func (t *Throttle) Fail(ip string) {
	t.mu.Lock()
	now := t.Now()
	for k, f := range t.perIP {
		if now.Sub(f.last) > time.Hour {
			delete(t.perIP, k)
		}
	}
	f := t.perIP[ip]
	if f == nil {
		f = &ipFailures{}
		t.perIP[ip] = f
	}
	f.count++
	f.last = now
	if f.count%5 == 0 {
		f.until = now.Add(time.Minute)
	}
	t.acctCount++
	if t.acctCount%20 == 0 {
		t.acctUntil = now.Add(5 * time.Minute)
		slog.Warn("sign-in paused for 5 minutes after repeated failures", "failures", t.acctCount)
	}
	delay := t.Delay
	t.mu.Unlock()
	time.Sleep(delay)
}

// Clear forgets failures after a successful sign-in.
func (t *Throttle) Clear(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.perIP, ip)
	t.acctCount = 0
	t.acctUntil = time.Time{}
}
