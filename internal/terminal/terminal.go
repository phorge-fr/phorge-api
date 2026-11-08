// Package terminal opens a terminal on an instance for a person, through Incus, and relays it to the console.
//
// The API holds no credential of Incus: it trades the token of the person for a token of the Incus application (RFC 8693,
// docs/decisions/0013), opens an interactive exec with it, and relays the two websockets Incus gives back. Incus decides, from the
// right of that person in its own store, and the API ends the session itself when the right is gone: Incus does not.
package terminal

import (
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// The codes a session closes with. 1000 is a session that ended by itself (the shell exited); the 4000s are the API's own, which
// the console shows with the readable reason that goes with them.
const (
	CodeEnded           websocket.StatusCode = websocket.StatusNormalClosure
	CodeIdle            websocket.StatusCode = 4001
	CodeTooLong         websocket.StatusCode = 4002
	CodeRightRemoved    websocket.StatusCode = 4003
	CodeInstanceStopped websocket.StatusCode = 4004
	CodeUnavailable     websocket.StatusCode = 4005
	CodeShutdown        websocket.StatusCode = 4006
)

// Reason is why a session closes: the code and the sentence the console shows, and what is logged.
type Reason struct {
	Code websocket.StatusCode
	// Text is read by the person: it never carries what Incus or Authentik said.
	Text string
	// Log is the short name of the reason in the audit log.
	Log string
}

// The reasons.
var (
	ReasonEnded           = Reason{CodeEnded, "The session ended.", "ended"}
	ReasonIdle            = Reason{CodeIdle, "The terminal was closed because nothing was typed or shown for too long.", "idle"}
	ReasonTooLong         = Reason{CodeTooLong, "The terminal was closed because it was open for the longest time allowed.", "max_duration"}
	ReasonRightRemoved    = Reason{CodeRightRemoved, "You no longer have the right to open a terminal on this instance.", "right_removed"}
	ReasonInstanceStopped = Reason{CodeInstanceStopped, "The instance is no longer running.", "instance_stopped"}
	ReasonUnavailable     = Reason{CodeUnavailable, "The terminal was closed because the platform could not check your rights.", "unavailable"}
	ReasonShutdown        = Reason{CodeShutdown, "The terminal was closed because the platform is restarting.", "shutdown"}
)

// Errors of opening a terminal. The API answers each with a sentence of its own: none of them carries what a system behind said.
var (
	// ErrRefused is the identity provider refusing to give the person a token for Incus.
	ErrRefused = errors.New("the identity provider refused the exchange")
	// ErrForbidden is Incus refusing the person an exec on that instance.
	ErrForbidden = errors.New("incus refused the exec")
	// ErrNotFound is Incus not knowing the instance.
	ErrNotFound = errors.New("incus does not know the instance")
	// ErrUnavailable is any other failure to reach a system behind.
	ErrUnavailable = errors.New("a system behind is unavailable")
)

// LimitError is a limit of the sessions that is reached.
type LimitError struct{ Message string }

func (e *LimitError) Error() string { return e.Message }

// Size is the size of the window of the terminal.
type Size struct{ Cols, Rows int }

// Valid reports whether a size is one a terminal can have.
func (s Size) Valid() bool { return s.Cols >= 1 && s.Cols <= 1000 && s.Rows >= 1 && s.Rows <= 1000 }

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.Cols, s.Rows) }

// Settings are the limits of the sessions, all of them settings of the deployment.
type Settings struct {
	// MaxPerUser and MaxPerTenant are how many sessions a person, and a tenant, may have open at once.
	MaxPerUser, MaxPerTenant int
	// Idle is how long a session may go without a byte either way, MaxDuration how long it may last, and Recheck how often the right
	// of the person is checked again while it is open.
	Idle, MaxDuration, Recheck time.Duration
}
