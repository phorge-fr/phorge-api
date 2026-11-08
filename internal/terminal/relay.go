package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Registry counts the sessions that are open, per person and per tenant, and refuses a new one past the limits.
type Registry struct {
	maxUser, maxTenant int

	mu      sync.Mutex
	byUser  map[string]int
	byTenat map[string]int
}

// NewRegistry returns a registry with the limits of s.
func NewRegistry(s Settings) *Registry {
	return &Registry{maxUser: s.MaxPerUser, maxTenant: s.MaxPerTenant, byUser: map[string]int{}, byTenat: map[string]int{}}
}

// Acquire takes a place for a person on a tenant, or says which limit is reached. The function it returns gives the place back and
// is safe to call twice.
func (r *Registry) Acquire(user, tenant string) (release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxUser > 0 && r.byUser[user] >= r.maxUser {
		return nil, &LimitError{Message: "you already have " + strconv.Itoa(r.maxUser) + " terminals open: close one first"}
	}
	if r.maxTenant > 0 && r.byTenat[tenant] >= r.maxTenant {
		return nil, &LimitError{Message: "this tenant already has " + strconv.Itoa(r.maxTenant) + " terminals open: close one first"}
	}
	r.byUser[user]++
	r.byTenat[tenant]++
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.byUser[user]--
			r.byTenat[tenant]--
			if r.byUser[user] <= 0 {
				delete(r.byUser, user)
			}
			if r.byTenat[tenant] <= 0 {
				delete(r.byTenat, tenant)
			}
		})
	}, nil
}

// Result is how a session went: why it closed, and for how long it was open.
type Result struct {
	Reason   Reason
	Duration time.Duration
}

// Recheck is asked, at the interval of the settings, whether the session may go on: it returns the reason to close it, or nil.
type Recheck func(ctx context.Context) *Reason

// resize is the message the console sends to change the size of the window.
type resize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// Relay carries a terminal between the console and Incus until one side ends, the person loses the right, the session is idle for
// too long or has lasted the longest time allowed, then closes both sides with the reason. Binary frames are the bytes of the
// terminal in both directions, text frames from the console are JSON controls (resize). What is typed or shown is never read, kept
// or logged here. It returns when everything is closed.
func Relay(parent context.Context, console *websocket.Conn, incus *Session, s Settings, recheck Recheck) Result {
	start := time.Now()
	// The reads and writes of the websockets do not use the context that says the session is over: the library closes a connection whose
	// read is cancelled, and the console has to be told why before that.
	ioCtx := context.WithoutCancel(parent)
	ctx, cancel := context.WithCancel(ioCtx)
	defer cancel()
	console.SetReadLimit(1 << 20)

	var (
		once   sync.Once
		reason = ReasonEnded
		last   atomic.Int64
		wg     sync.WaitGroup
	)
	last.Store(start.UnixNano())
	end := func(r Reason) {
		once.Do(func() {
			reason = r
			cancel()
		})
	}
	touch := func() { last.Store(time.Now().UnixNano()) }

	// What the person types, and the size of the window, go to Incus.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			typ, data, err := console.Read(ioCtx)
			if err != nil {
				// The console went away, or the session was ended from here: either way it is over.
				end(ReasonEnded)
				return
			}
			touch()
			switch typ {
			case websocket.MessageBinary:
				if err := incus.Stdio.Write(ioCtx, websocket.MessageBinary, data); err != nil {
					end(ReasonEnded)
					return
				}
			case websocket.MessageText:
				var m resize
				if json.Unmarshal(data, &m) != nil || m.Type != "resize" || !(Size{m.Cols, m.Rows}).Valid() {
					continue // a control that is not understood is ignored
				}
				msg, _ := json.Marshal(map[string]any{"command": "window-resize", "args": map[string]string{"width": strconv.Itoa(m.Cols), "height": strconv.Itoa(m.Rows)}})
				if err := incus.Control.Write(ioCtx, websocket.MessageText, msg); err != nil {
					end(ReasonEnded)
					return
				}
			}
		}
	}()

	// What the terminal shows goes to the console. When Incus closes it, the shell has exited.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			typ, data, err := incus.Stdio.Read(ioCtx)
			if err != nil {
				end(ReasonEnded)
				return
			}
			touch()
			if typ != websocket.MessageBinary {
				continue
			}
			if err := console.Write(ioCtx, websocket.MessageBinary, data); err != nil {
				end(ReasonEnded)
				return
			}
		}
	}()

	// The platform stopping ends the session with its own reason.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-parent.Done():
			end(ReasonShutdown)
		case <-ctx.Done():
		}
	}()

	// Incus does not end a session whose right was removed (measured), nor does it know of our limits: the checks are here.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := s.Recheck
		if s.Idle/4 < tick || tick <= 0 {
			tick = s.Idle / 4
		}
		if tick <= 0 {
			tick = time.Second
		}
		if s.MaxDuration > 0 && s.MaxDuration/4 < tick {
			tick = s.MaxDuration / 4
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		lastCheck := start
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				switch {
				case s.MaxDuration > 0 && now.Sub(start) >= s.MaxDuration:
					end(ReasonTooLong)
					return
				case s.Idle > 0 && now.Sub(time.Unix(0, last.Load())) >= s.Idle:
					end(ReasonIdle)
					return
				case recheck != nil && s.Recheck > 0 && now.Sub(lastCheck) >= s.Recheck:
					lastCheck = now
					if r := recheck(ioCtx); r != nil {
						end(*r)
						return
					}
				}
			}
		}
	}()

	<-ctx.Done()
	// Say why to the console first, then let go of Incus, which ends the process.
	closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = closeCtxWait(closeCtx, func() error { return console.Close(reason.Code, reason.Text) })
	incus.Close()
	wg.Wait()
	return Result{Reason: reason, Duration: time.Since(start)}
}

// closeCtxWait runs f and gives up waiting for it when ctx ends.
func closeCtxWait(ctx context.Context, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.New("close timed out")
	}
}
