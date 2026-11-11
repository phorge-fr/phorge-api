package quota

import (
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

// The limits are written twice, here and in the Tenant composition of Hangar (composition-tenant.yml): this is what the composition
// says. Its project limit for the disk is the 128 GiB plus 512 MiB for the state of each of the 2 VMs, 132096 MiB. The profiles are
// the API's own: Incus has no limit for them.
func TestTheLimitsAreTheOnesOfTheTenantComposition(t *testing.T) {
	want := Limits{Instances: 2, VCPU: 2, MemoryMiB: 4096, DiskGiB: 128, Networks: 2, Profiles: 2}
	if PerTenant != want {
		t.Errorf("PerTenant = %+v, want %+v", PerTenant, want)
	}
	if project := PerTenant.DiskGiB*1024 + 512*PerTenant.Instances; project != 132096 {
		t.Errorf("the project limit for the disk would be %d MiB, the composition has 132096", project)
	}
}

// The CPU, the memory and the disk hold two instances of the size of the profile s (or one of m), and nothing that is bigger.
func TestTheLimitsHoldTwoOfTheProfileS(t *testing.T) {
	m, ok := compute.ProfileNamed("s")
	if !ok {
		t.Fatal("no profile s")
	}
	l := PerTenant
	if l.VCPU != 2*m.CPU || l.MemoryMiB != 2*m.MemoryMiB || l.DiskGiB != 2*m.DiskGiB {
		t.Errorf("%+v does not hold two s (%+v)", l, m)
	}
	var used Used
	for i := 0; i < 2; i++ {
		if e := CheckInstance(used, m.CPU, m.MemoryMiB, m.DiskGiB); e != nil {
			t.Errorf("instance %d of the profile s is refused: %v", i+1, e)
		}
		used.Instances++
		used.VCPU += m.CPU
		used.MemoryMiB += m.MemoryMiB
		used.DiskGiB += m.DiskGiB
	}
	if e := CheckInstance(used, m.CPU, m.MemoryMiB, m.DiskGiB); e == nil {
		t.Error("a third instance of the profile s is accepted")
	}
	// one m is all the CPU, the memory and the disk there is
	if mm, _ := compute.ProfileNamed("m"); mm.CPU != PerTenant.VCPU || mm.MemoryMiB != PerTenant.MemoryMiB || mm.DiskGiB != PerTenant.DiskGiB {
		t.Errorf("one m (%+v) is not the whole of %+v", mm, PerTenant)
	}
	// a profile bigger than the limits is refused
	big := compute.BuiltinProfiles[2]
	if e := CheckInstance(Used{}, big.CPU, big.MemoryMiB, big.DiskGiB); e == nil {
		t.Error("a l is accepted")
	}
}

func TestUsedByCountsStoppedInstancesAndProfiles(t *testing.T) {
	got := UsedBy(
		[]compute.Instance{
			{Name: "a", CPU: 2, MemoryMiB: 2048, DiskGiB: 20},
			{Name: "b", Profile: "m"}, // 2 vCPU, 4 GiB, 128 GiB
			{Name: "c", CPU: 1, MemoryMiB: 1024, DiskGiB: 10, Running: false},
		},
		[]compute.Network{{Name: "n"}},
		[]compute.Profile{{Name: "web", CPU: 1, MemoryMiB: 1024, DiskGiB: 10}},
	)
	want := Used{Instances: 3, VCPU: 5, MemoryMiB: 2048 + 4096 + 1024, DiskGiB: 158, Networks: 1, Profiles: 1}
	if got != want {
		t.Errorf("UsedBy = %+v, want %+v", got, want)
	}
}

func TestCheckInstanceSaysWhichLimitIsPassed(t *testing.T) {
	for name, tt := range map[string]struct {
		used             Used
		cpu, mem, disk   int
		limit, mentioned string
	}{
		"nothing is passed":     {Used{Instances: 1, VCPU: 1, MemoryMiB: 1024, DiskGiB: 10}, 1, 1024, 10, "", ""},
		"exactly the limits":    {Used{}, 2, 4096, 128, "", ""},
		"the count":             {Used{Instances: 2}, 1, 1024, 10, "instances", "2 instances out of 2 allowed: delete one first"},
		"the CPU":               {Used{Instances: 1, VCPU: 1}, 2, 1024, 10, "vcpu", "needs 2 vCPU and the tenant has 1 out of 2"},
		"the memory":            {Used{Instances: 1, MemoryMiB: 3072}, 2, 2048, 10, "memoryMiB", "2 GiB of memory and the tenant has 3 GiB out of 4 GiB"},
		"the disk":              {Used{}, 1, 1024, 200, "diskGiB", "200 GiB of disk and the tenant has 0 out of 128 GiB"},
		"the count comes first": {Used{Instances: 2, VCPU: 2}, 16, 65536, 500, "instances", ""},
	} {
		e := CheckInstance(tt.used, tt.cpu, tt.mem, tt.disk)
		switch {
		case tt.limit == "" && e != nil:
			t.Errorf("%s: refused: %v", name, e)
		case tt.limit != "" && e == nil:
			t.Errorf("%s: accepted", name)
		case e != nil && e.Limit != tt.limit:
			t.Errorf("%s: limit %q, want %q", name, e.Limit, tt.limit)
		case e != nil && !strings.Contains(e.Message, tt.mentioned):
			t.Errorf("%s: message %q does not say %q", name, e.Message, tt.mentioned)
		}
	}
}

func TestCheckNetworkProfileAndCount(t *testing.T) {
	if e := CheckNetwork(Used{Networks: 2}); e == nil || e.Limit != "networks" || !strings.Contains(e.Message, "2 networks out of 2 allowed: delete one first") {
		t.Errorf("a third network: %v", e)
	}
	if e := CheckNetwork(Used{Networks: 1}); e != nil {
		t.Errorf("a second network: %v", e)
	}
	if e := CheckProfile(Used{Profiles: 2}); e == nil || e.Limit != "profiles" || !strings.Contains(e.Message, "2 profiles out of 2 allowed") || !strings.Contains(e.Message, "delete one first") {
		t.Errorf("a third profile: %v", e)
	}
	if e := CheckProfile(Used{Profiles: 1}); e != nil {
		t.Errorf("a second profile: %v", e)
	}
	if e := CheckCount("images", 20, 20, "this tenant holds too many images"); e == nil || e.Limit != "images" || !strings.Contains(e.Message, "20 out of 20") {
		t.Errorf("the 21st image: %v", e)
	}
	if e := CheckCount("images", 19, 20, "x"); e != nil {
		t.Errorf("the 20th image: %v", e)
	}
	if e := CheckCount("images", 1000, 0, "x"); e != nil {
		t.Errorf("0 is no limit: %v", e)
	}
}

func TestSizeAskedTakesTheProfileOrTheDefaults(t *testing.T) {
	two := 2
	for name, tt := range map[string]struct {
		req           compute.CreateInstance
		cpu, mem, dsk int
	}{
		"a profile":  {compute.CreateInstance{Profile: "l"}, 4, 8192, 256},
		"nothing":    {compute.CreateInstance{}, 1, 1024, 10},
		"only a cpu": {compute.CreateInstance{CPU: &two}, 2, 1024, 10},
	} {
		cpu, mem, dsk := SizeAsked(tt.req, nil)
		if cpu != tt.cpu || mem != tt.mem || dsk != tt.dsk {
			t.Errorf("%s: %d/%d/%d, want %d/%d/%d", name, cpu, mem, dsk, tt.cpu, tt.mem, tt.dsk)
		}
	}
}

func TestAProfileTheUserMadeCountsWhatItComesTo(t *testing.T) {
	mine := []compute.Profile{{Name: "web", CPU: 3, MemoryMiB: 3072, DiskGiB: 30}}
	got := UsedBy([]compute.Instance{{Name: "a", Profile: "web"}, {Name: "b", Profile: "s"}}, nil, mine)
	if want := (Used{Instances: 2, VCPU: 4, MemoryMiB: 3072 + 2048, DiskGiB: 94, Profiles: 1}); got != want {
		t.Errorf("UsedBy = %+v, want %+v", got, want)
	}
	if cpu, mem, disk := SizeAsked(compute.CreateInstance{Profile: "web"}, mine); cpu != 3 || mem != 3072 || disk != 30 {
		t.Errorf("SizeAsked = %d/%d/%d", cpu, mem, disk)
	}
	// without the profile in the list it counts nothing, as an instance whose profile is gone does
	if got := UsedBy([]compute.Instance{{Name: "a", Profile: "web"}}, nil, nil); got.VCPU != 0 {
		t.Errorf("an unknown profile counted %+v", got)
	}
}
