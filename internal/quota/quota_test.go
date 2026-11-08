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
		tenants.Small:  {1, 2, 4096, 20, 1},
		tenants.Medium: {3, 4, 8192, 100, 2},
		tenants.Large:  {6, 8, 16384, 250, 3},
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
		"exactly the plan":      {tenants.Small, Used{}, 2, 4096, 20, "", ""},
		"the count":             {tenants.Small, Used{Instances: 1}, 1, 1024, 10, "instances", "1 instance out of 1"},
		"the count, several":    {tenants.Medium, Used{Instances: 3}, 1, 1024, 10, "instances", "3 instances out of 3"},
		"the CPU":               {tenants.Medium, Used{Instances: 1, VCPU: 3}, 2, 1024, 10, "vcpu", "needs 2 vCPU and the tenant has 3 out of 4"},
		"the memory":            {tenants.Medium, Used{Instances: 1, MemoryMiB: 7168}, 1, 2048, 10, "memoryMiB", "2 GiB of memory and the tenant has 7 GiB out of 8 GiB"},
		"the disk":              {tenants.Small, Used{}, 1, 1024, 64, "diskGiB", "64 GiB of disk and the tenant has 0 out of 20 GiB"},
		"the count comes first": {tenants.Small, Used{Instances: 1, VCPU: 2}, 16, 65536, 500, "instances", ""},
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
