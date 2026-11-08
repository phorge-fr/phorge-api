package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

func TestProfilesWhoMayRead(t *testing.T) {
	for who, want := range map[string]int{
		"viewer": 200, "user": 200, "operator": 200, "admin": 200,
		"svc":        403, // a key that writes instances reads nothing of them
		"svc_images": 403, "mallory": 404, "": 401,
	} {
		e := newEnv(t)
		if code := call(t, e, "GET", "/v1/tenants/demo/profiles", who, ""); code != want {
			t.Errorf("%q: status %d, want %d", who, code, want)
		}
	}
}

func TestProfilesAreThreeSizes(t *testing.T) {
	e := newEnv(t)
	var body struct {
		Items []profileBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/profiles", "viewer", ""), &body)
	want := []profileBody{
		{Name: "s", Description: "1 vCPU, 2 GiB RAM, 64 GiB disk", CPU: 1, MemoryMiB: 2048, DiskGiB: 64, Builtin: true},
		{Name: "m", Description: "2 vCPU, 4 GiB RAM, 128 GiB disk", CPU: 2, MemoryMiB: 4096, DiskGiB: 128, Builtin: true},
		{Name: "l", Description: "4 vCPU, 8 GiB RAM, 256 GiB disk", CPU: 4, MemoryMiB: 8192, DiskGiB: 256, Builtin: true},
	}
	if len(body.Items) != 3 {
		t.Fatalf("items = %+v", body.Items)
	}
	for i := range want {
		if body.Items[i] != want[i] {
			t.Errorf("profile %d = %+v, want %+v", i, body.Items[i], want[i])
		}
	}
}

// The numbers of the profiles are written twice, here and in the Tenant composition of Hangar (composition-tenant.yml): what the
// description says is what the fields hold.
func TestTheBuiltinProfilesDescribeTheirOwnNumbers(t *testing.T) {
	for _, p := range compute.BuiltinProfiles {
		want := fmt.Sprintf("%d vCPU, %d GiB RAM, %d GiB disk", p.CPU, p.MemoryMiB/1024, p.DiskGiB)
		if p.Description != want {
			t.Errorf("profile %s: description %q, numbers say %q", p.Name, p.Description, want)
		}
	}
}

func TestLimitsNeedACredentialAndSayWhatTheAPIEnforces(t *testing.T) {
	e := newEnv(t)
	if code := call(t, e, "GET", "/v1/limits", "", ""); code != http.StatusUnauthorized {
		t.Errorf("without a credential: status %d, want 401", code)
	}
	var body limitsBody
	// Anyone signed in: the numbers hold nothing about a tenant.
	mustDecode(t, e.do(t, "GET", "/v1/limits", "mallory", ""), &body)
	if body.Instance.CPU != (compute.Limit{Min: 1, Max: 16, Default: 1}) ||
		body.Instance.MemoryMiB != (compute.Limit{Min: 256, Max: 65536, Default: 1024}) ||
		body.Instance.DiskGiB != (compute.Limit{Min: 1, Max: 500, Default: 10}) {
		t.Errorf("instance limits = %+v", body.Instance)
	}
	if body.Instance.MaxSSHKeys != 20 || body.Network.MaxNameLength != 15 || body.Network.MaxDescriptionLength != 200 {
		t.Errorf("limits = %+v", body)
	}
}

func TestCreateInstanceFromAProfile(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian","profile":"m"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := e.instances.created[0]
	if got.Profile != "m" || got.CPU != nil || got.MemoryMiB != nil || got.DiskGiB != nil {
		t.Errorf("the store received %+v: a profile is the whole size", got)
	}
	var body instanceBody
	mustDecode(t, rec, &body)
	if body.Profile != "m" || body.CPU != 2 || body.MemoryMiB != 4096 || body.DiskGiB != 128 {
		t.Errorf("body = %+v: the profile, and what it comes to", body)
	}
}

func TestCreateInstanceRefusesAProfileWithASizeOrAnUnknownProfile(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	for name, tt := range map[string]struct{ body, want string }{
		"a profile and a cpu":    {`{"name":"a","image":"debian","profile":"m","cpu":2}`, "not both"},
		"a profile and a memory": {`{"name":"a","image":"debian","profile":"m","memoryMiB":2048}`, "not both"},
		"a profile and a disk":   {`{"name":"a","image":"debian","profile":"m","diskGiB":20}`, "not both"},
		"an unknown profile":     {`{"name":"a","image":"debian","profile":"xl"}`, "profile xl does not exist in this tenant: s, m, l"},
		"a bad profile name":     {`{"name":"a","image":"debian","profile":"-m"}`, "profile must be the name"},
	} {
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", tt.body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: status %d: %s, want 422 with %q", name, rec.Code, rec.Body, tt.want)
		}
	}
	if len(e.instances.created) != 0 {
		t.Errorf("something reached the store: %+v", e.instances.created)
	}
}

func TestPatchMovesAnInstanceBetweenAProfileAndItsOwnSize(t *testing.T) {
	// A fresh environment for each case: a patch changes the instance the next one finds.
	patch := func(t *testing.T, in compute.Instance, body string) (*httptest.ResponseRecorder, *instanceStore) {
		t.Helper()
		e := newEnv(t)
		in.Name = "vm"
		in.Image = "debian"
		e.instances.put("demo", in)
		return e.do(t, "PATCH", "/v1/tenants/demo/instances/vm", "user", body), e.instances
	}
	custom := compute.Instance{CPU: 1, MemoryMiB: 1024, DiskGiB: 10}
	sized := compute.Instance{Profile: "s"}

	t.Run("to a profile", func(t *testing.T) {
		rec, st := patch(t, custom, `{"profile":"m"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if p := st.patched[0]; p.Profile == nil || *p.Profile != "m" || p.DropProfile {
			t.Errorf("patch = %+v", p)
		}
	})
	t.Run("out of a profile with the three values together", func(t *testing.T) {
		rec, st := patch(t, sized, `{"cpu":2,"memoryMiB":2048,"diskGiB":20}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if p := st.patched[0]; !p.DropProfile || p.Profile != nil {
			t.Errorf("patch = %+v: the profile must be dropped with the three values", p)
		}
	})
	t.Run("a value alone on an instance that has a profile", func(t *testing.T) {
		rec, st := patch(t, sized, `{"cpu":4}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "has the profile s") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
		if len(st.patched) != 0 {
			t.Errorf("something reached the store: %+v", st.patched)
		}
	})
	t.Run("a profile and a value", func(t *testing.T) {
		rec, _ := patch(t, custom, `{"profile":"s","cpu":4}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not both") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("an unknown profile", func(t *testing.T) {
		rec, _ := patch(t, custom, `{"profile":"xl"}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "profile xl does not exist") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("a value alone on an instance that has none is what it always was", func(t *testing.T) {
		rec, st := patch(t, custom, `{"cpu":4}`)
		if rec.Code != http.StatusOK || st.patched[0].DropProfile {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
}
