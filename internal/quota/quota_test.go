package quota

import (
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// The plans are written twice, here and in the Tenant composition of Hangar: this is the table the composition says.
func TestThePlansAreTheOnesOfTheTenantComposition(t *testing.T) {
	want := map[tenants.Plan]Limits{
		tenants.Small:  {2, 2, 4096, 128, 1},
		tenants.Medium: {4, 4, 8192, 256, 2},
		tenants.Large:  {6, 8, 16384, 512, 3},
	}
	for plan, w := range want {
		if got := LimitsOf(plan); got != w {
			t.Errorf("%s = %+v, want %+v", plan, got, w)
		}
	}
	if (LimitsOf("xl") != Limits{}) {
		t.Error("an unknown plan holds something")
	}
}

// The CPU, the memory and the disk of a plan hold two instances of the size of its own profile, and nothing that is bigger: this is
// what the alignment says. The count of instances is the plan's own, 2, 4 and 6.
func TestEachPlanHoldsTwoOfItsOwnProfile(t *testing.T) {
	counts := map[tenants.Plan]int{tenants.Small: 2, tenants.Medium: 4, tenants.Large: 6}
	for plan, name := range map[tenants.Plan]string{tenants.Small: "s", tenants.Medium: "m", tenants.Large: "l"} {
		p, ok := compute.ProfileNamed(name)
		if !ok {
			t.Fatalf("no profile %s", name)
		}
		l := LimitsOf(plan)
		if l.Instances != counts[plan] {
			t.Errorf("plan %s allows %d instances, want %d", plan, l.Instances, counts[plan])
		}
		if l.VCPU != 2*p.CPU || l.MemoryMiB < 2*p.MemoryMiB || l.DiskGiB < 2*p.DiskGiB {
			t.Errorf("plan %s %+v does not hold two %s (%+v)", plan, l, name, p)
		}
		var used Used
		for i := 0; i < 2; i++ {
			if e := CheckInstance(plan, used, p.CPU, p.MemoryMiB, p.DiskGiB); e != nil {
				t.Errorf("plan %s refuses instance %d of its own profile %s: %v", plan, i+1, name, e)
			}
			used.Instances++
			used.VCPU += p.CPU
			used.MemoryMiB += p.MemoryMiB
			used.DiskGiB += p.DiskGiB
		}
		// a third of the same size passes the CPU, the memory or the disk, whichever gives out first
		if e := CheckInstance(plan, used, p.CPU, p.MemoryMiB, p.DiskGiB); e == nil {
			t.Errorf("plan %s accepts a third instance of its own profile", plan)
		}
	}
	// a profile bigger than the plan is refused
	l, small := compute.BuiltinProfiles[2], tenants.Small
	if e := CheckInstance(small, Used{}, l.CPU, l.MemoryMiB, l.DiskGiB); e == nil {
		t.Error("a l on a small plan is accepted")
	}
	// the count is reached only by instances smaller than the profile
	if e := CheckInstance(tenants.Medium, Used{Instances: 4}, 1, 256, 1); e == nil || e.Limit != "instances" {
		t.Errorf("a fifth instance on a medium plan: %v", e)
	}
	if e := CheckInstance(tenants.Large, Used{Instances: 5}, 1, 256, 1); e != nil {
		t.Errorf("a sixth instance on a large plan: %v", e)
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
		nil,
	)
	want := Used{Instances: 3, VCPU: 5, MemoryMiB: 2048 + 4096 + 1024, DiskGiB: 158, Networks: 1}
	if got != want {
		t.Errorf("UsedBy = %+v, want %+v", got, want)
	}
}

func TestCheckInstanceSaysWhichLimitIsPassed(t *testing.T) {
	for name, tt := range map[string]struct {
		plan             tenants.Plan
		used             Used
		cpu, mem, disk   int
		limit, mentioned string
	}{
		"nothing is passed":     {tenants.Medium, Used{Instances: 1, VCPU: 1, MemoryMiB: 1024, DiskGiB: 10}, 1, 1024, 10, "", ""},
		"exactly the plan":      {tenants.Small, Used{}, 2, 4096, 128, "", ""},
		"the count":             {tenants.Small, Used{Instances: 2}, 1, 1024, 10, "instances", "2 instances out of 2"},
		"the count on medium":   {tenants.Medium, Used{Instances: 4}, 1, 1024, 10, "instances", "4 instances out of 4"},
		"the CPU":               {tenants.Medium, Used{Instances: 1, VCPU: 3}, 2, 1024, 10, "vcpu", "needs 2 vCPU and the tenant has 3 out of 4"},
		"the memory":            {tenants.Medium, Used{Instances: 1, MemoryMiB: 7168}, 1, 2048, 10, "memoryMiB", "2 GiB of memory and the tenant has 7 GiB out of 8 GiB"},
		"the disk":              {tenants.Small, Used{}, 1, 1024, 200, "diskGiB", "200 GiB of disk and the tenant has 0 out of 128 GiB"},
		"the count comes first": {tenants.Small, Used{Instances: 2, VCPU: 2}, 16, 65536, 500, "instances", ""},
	} {
		e := CheckInstance(tt.plan, tt.used, tt.cpu, tt.mem, tt.disk)
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

func TestCheckNetworkAndCount(t *testing.T) {
	if e := CheckNetwork(tenants.Small, Used{Networks: 1}); e == nil || e.Limit != "networks" || !strings.Contains(e.Message, "1 network out of 1") {
		t.Errorf("a second network on a small plan: %v", e)
	}
	if e := CheckNetwork(tenants.Large, Used{Networks: 2}); e != nil {
		t.Errorf("a third network on a large plan: %v", e)
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
	if want := (Used{Instances: 2, VCPU: 4, MemoryMiB: 3072 + 2048, DiskGiB: 94}); got != want {
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
