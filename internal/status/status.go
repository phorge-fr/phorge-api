// Package status turns what Crossplane reports about an object into the phase a client polls.
package status

import "time"

// Phase is where an object is, for a client that polls it.
type Phase string

// The phases.
const (
	// Provisioning: the composed resources are being created or updated. Nothing is wrong yet.
	Provisioning Phase = "Provisioning"
	// Ready: everything the object is made of is up.
	Ready Phase = "Ready"
	// Failed: it has not become ready and there are problems, for longer than FailedAfter.
	Failed Phase = "Failed"
	// Deleting: it is being deleted, which takes up to a couple of minutes (Incus refuses to delete a project that is not
	// empty, so it is retried until it is).
	Deleting Phase = "Deleting"
)

// FailedAfter is how long an object may be not ready with problems before it is called failed: the first reconciles of
// Crossplane report problems that go away by themselves.
const FailedAfter = 3 * time.Minute

// Object is what the phase is computed from.
type Object struct {
	// Ready is the Ready condition, NotReadySince when it last changed (the creation time if it never did).
	Ready         bool
	NotReadySince time.Time
	// Problems is status.problems: the Incus or OpenFGA error of a composed resource, empty when everything is up.
	Problems string
	Deleting bool
}

// At is the phase of the object at now.
func (o Object) At(now time.Time) Phase {
	switch {
	case o.Deleting:
		return Deleting
	case o.Ready:
		return Ready
	case o.Problems != "" && now.Sub(o.NotReadySince) >= FailedAfter:
		return Failed
	default:
		return Provisioning
	}
}
