// Package quota is what a tenant may hold, and what it holds. The plan of a tenant is enforced by Incus as limits of its project;
// this package knows the same numbers so that the API can say "the limit is reached" before it asks, in words and with a stable
// code, and so that a client can draw what is left. Incus stays the authority: a request that passes here can still be refused
// there, and its refusal is told in words by internal/status.
package quota

import (
	"fmt"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// Limits are the limits of a plan. They are the ones of the Tenant composition in Hangar (composition-tenant.yml): the two lists
// are the same. The CPU, the memory and the disk of each plan hold two instances of the size of its profile (small two s, medium two m, large two l); the count of instances is 2, 4 and 6.
type Limits struct {
	Instances int `json:"instances"`
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memoryMiB"`
	DiskGiB   int `json:"diskGiB"`
	Networks  int `json:"networks"`
}

// Plans are the quota plans of the Tenant XRD.
var Plans = map[tenants.Plan]Limits{
	tenants.Small:  {Instances: 2, VCPU: 2, MemoryMiB: 4 * 1024, DiskGiB: 128, Networks: 1},
	tenants.Medium: {Instances: 4, VCPU: 4, MemoryMiB: 8 * 1024, DiskGiB: 256, Networks: 2},
	tenants.Large:  {Instances: 6, VCPU: 8, MemoryMiB: 16 * 1024, DiskGiB: 512, Networks: 3},
}

// LimitsOf returns the limits of a plan. An unknown plan holds nothing: it is the plan of a tenant that the XRD would refuse.
func LimitsOf(p tenants.Plan) Limits { return Plans[p] }

// Used is what a tenant holds. A stopped instance counts: Incus counts it.
type Used = Limits

// UsedBy adds up the instances and the networks of a tenant. An instance that has a profile counts what the profile comes to: the
// three every tenant has, or one of the profiles the user made (user).
func UsedBy(instances []compute.Instance, networks []compute.Network, user []compute.Profile) Used {
	u := Used{Instances: len(instances), Networks: len(networks)}
	for _, in := range instances {
		cpu, mem, disk := SizeOf(in, user)
		u.VCPU += cpu
		u.MemoryMiB += mem
		u.DiskGiB += disk
	}
	return u
}

// SizeOf is the CPU, the memory and the disk an instance comes to, its profile's when it has one.
func SizeOf(in compute.Instance, user []compute.Profile) (cpu, memMiB, diskGiB int) {
	if p, ok := compute.Find(user, in.Profile); ok && in.Profile != "" {
		return p.CPU, p.MemoryMiB, p.DiskGiB
	}
	return in.CPU, in.MemoryMiB, in.DiskGiB
}

// SizeAsked is the size a request for an instance comes to, with the defaults of what it leaves out.
func SizeAsked(r compute.CreateInstance, user []compute.Profile) (cpu, memMiB, diskGiB int) {
	if p, ok := compute.Find(user, r.Profile); ok && r.Profile != "" {
		return p.CPU, p.MemoryMiB, p.DiskGiB
	}
	r = r.WithDefaultSize()
	return *r.CPU, *r.MemoryMiB, *r.DiskGiB
}

// Error says which limit a request would pass, and by how much. It is a refusal of the API, not of Incus.
type Error struct {
	// Limit is the name of the limit: instances, vcpu, memoryMiB, diskGiB, networks, images, tenants.
	Limit string
	// Message is the sentence for the user.
	Message string
}

func (e *Error) Error() string { return e.Message }

// CheckInstance returns an *Error when an instance of that size would pass a limit of the plan. The count comes first, then the
// CPU, the memory and the disk, so that the answer is the first thing the user can do something about.
func CheckInstance(plan tenants.Plan, used Used, cpu, memMiB, diskGiB int) *Error {
	l := LimitsOf(plan)
	switch {
	case used.Instances+1 > l.Instances:
		return &Error{"instances", fmt.Sprintf("this tenant has %d %s out of %d allowed by its plan %s: delete one first", used.Instances, plural(used.Instances, "instance", "instances"), l.Instances, plan)}
	case used.VCPU+cpu > l.VCPU:
		return &Error{"vcpu", fmt.Sprintf("this instance needs %d vCPU and the tenant has %d out of %d allowed by its plan %s", cpu, used.VCPU, l.VCPU, plan)}
	case used.MemoryMiB+memMiB > l.MemoryMiB:
		return &Error{"memoryMiB", fmt.Sprintf("this instance needs %s of memory and the tenant has %s out of %s allowed by its plan %s", gib(memMiB), gib(used.MemoryMiB), gib(l.MemoryMiB), plan)}
	case used.DiskGiB+diskGiB > l.DiskGiB:
		return &Error{"diskGiB", fmt.Sprintf("this instance needs %d GiB of disk and the tenant has %d out of %d GiB allowed by its plan %s", diskGiB, used.DiskGiB, l.DiskGiB, plan)}
	}
	return nil
}

// CheckNetwork returns an *Error when one more network would pass the limit of the plan.
func CheckNetwork(plan tenants.Plan, used Used) *Error {
	if l := LimitsOf(plan); used.Networks+1 > l.Networks {
		return &Error{"networks", fmt.Sprintf("this tenant has %d %s out of %d allowed by its plan %s: delete one first", used.Networks, plural(used.Networks, "network", "networks"), l.Networks, plan)}
	}
	return nil
}

// CheckCount returns an *Error when one more of something would pass a limit that is not the plan's: images of a tenant, tenants of a
// person. A limit of 0 is no limit.
func CheckCount(limit string, have, max int, what string) *Error {
	if max > 0 && have+1 > max {
		return &Error{limit, fmt.Sprintf("%s: %d out of %d allowed", what, have, max)}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func gib(mib int) string {
	if mib%1024 == 0 {
		return fmt.Sprintf("%d GiB", mib/1024)
	}
	return fmt.Sprintf("%d MiB", mib)
}
