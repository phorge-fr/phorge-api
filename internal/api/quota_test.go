package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/quota"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// tiny is a tenant like any other (2 instances, 2 vCPU, 4 GiB, 128 GiB, 2 networks, 2 profiles) that alice may use as she uses demo.
func withTiny(e *env) {
	e.store.tenants["tiny"] = tenants.Tenant{Name: "tiny", Owner: "alice@example.org", Plan: tenants.Small, CreatedAt: now, Ready: true, NotReadySince: now}
	for _, rel := range []string{"can_read_tenant", "can_read_instances", "can_write_instances", "can_read_images", "can_write_images", "can_read_networks", "can_write_networks", "can_read_profiles", "can_write_profiles"} {
		e.fga.grants["user:operator@example.org "+rel+" tenant:tiny"] = true
	}
	e.fga.objects["user:operator@example.org"] = append(e.fga.objects["user:operator@example.org"], "tenant:tiny")
	e.images.put("tiny", readyImage("debian"))
}

func TestQuotaSaysWhatEveryTenantMayHoldAndWhatItHolds(t *testing.T) {
	e := newEnv(t)
	e.instances.put("demo", compute.Instance{Name: "a", Image: "debian", CPU: 2, MemoryMiB: 2048, DiskGiB: 20})
	e.instances.put("demo", compute.Instance{Name: "b", Image: "debian", Profile: "m", Running: false}) // stopped: it counts
	var got quotaBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/quota", "viewer", ""), &got)
	// the plan of demo is large and the limits are the same as for every tenant: the plan is shown, deprecated, and limits nothing
	if got.Plan != tenants.Large || got.Limits != (quota.Limits{Instances: 2, VCPU: 2, MemoryMiB: 4096, DiskGiB: 128, Networks: 2, Profiles: 2}) {
		t.Errorf("plan and limits: %+v", got)
	}
	if want := (quota.Used{Instances: 2, VCPU: 4, MemoryMiB: 2048 + 4096, DiskGiB: 148, Networks: 1, Profiles: 0}); got.Used != want {
		t.Errorf("used = %+v, want %+v (the network of the test environment, the stopped VM, the profile)", got.Used, want)
	}
}

// The plan of a tenant is kept in the answer and limits nothing: a small one and a large one have the same limits.
func TestThePlanOfATenantChangesNoLimit(t *testing.T) {
	e := newEnv(t)
	withTiny(e) // small, demo is large
	var small, large quotaBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/tiny/quota", "operator", ""), &small)
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/quota", "viewer", ""), &large)
	if small.Plan != tenants.Small || large.Plan != tenants.Large || small.Limits != large.Limits || small.Limits != quota.PerTenant {
		t.Errorf("small %+v, large %+v: the limits are the same, whatever the plan", small, large)
	}
}

// The profiles are in what a tenant holds, but the three every tenant has are not.
func TestQuotaCountsTheProfilesTheUsersMade(t *testing.T) {
	e := newEnv(t)
	e.profiles.put("demo", madeProfile("one", 1, 1024, 10))
	var got quotaBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/quota", "viewer", ""), &got)
	if got.Used.Profiles != 1 || got.Limits.Profiles != 2 {
		t.Errorf("profiles used %d of %d, want 1 of 2: s, m and l are not counted", got.Used.Profiles, got.Limits.Profiles)
	}
}

func TestQuotaWhoMayRead(t *testing.T) {
	for who, want := range map[string]int{"viewer": 200, "user": 200, "operator": 200, "admin": 200, "svc_images": 403, "mallory": 404, "": 401} {
		if code := call(t, newEnv(t), "GET", "/v1/tenants/demo/quota", who, ""); code != want {
			t.Errorf("%q: status %d, want %d", who, code, want)
		}
	}
	if code := call(t, newEnv(t), "GET", "/v1/tenants/ghost/quota", "admin", ""); code != http.StatusNotFound {
		t.Errorf("a tenant that does not exist: %d", code)
	}
}

func quotaCode(t *testing.T, body string) string {
	t.Helper()
	if !strings.Contains(body, `"quota_exceeded"`) {
		t.Fatalf("not a quota refusal: %s", body)
	}
	return body
}

func TestAnInstanceThatWouldPassTheLimitsIsRefusedWithTheLimit(t *testing.T) {
	e := newEnv(t)
	withTiny(e)
	e.networks.put("tiny", readyNetwork("net"))
	// 2 instances are allowed
	for _, n := range []string{"a", "b"} {
		if code := call(t, e, "POST", "/v1/tenants/tiny/instances", "operator", `{"name":"`+n+`","image":"debian"}`); code != http.StatusAccepted {
			t.Fatalf("instance %s: status %d", n, code)
		}
	}
	rec := e.do(t, "POST", "/v1/tenants/tiny/instances", "operator", `{"name":"c","image":"debian"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), "2 instances out of 2") {
		t.Errorf("the third: status %d: %s", rec.Code, rec.Body)
	}
	if len(e.instances.created) != 2 {
		t.Errorf("the refused instance reached the store: %d created", len(e.instances.created))
	}
}

func TestAnInstanceBiggerThanTheLimitsIsRefusedBeforeIncusSeesIt(t *testing.T) {
	for name, tt := range map[string]struct{ body, want string }{
		"a disk":                    {`{"name":"a","image":"debian","diskGiB":200}`, "200 GiB of disk and the tenant has 0 out of 128 GiB"},
		"a profile that is too big": {`{"name":"a","image":"debian","profile":"l"}`, "4 vCPU"},
		"a cpu":                     {`{"name":"a","image":"debian","cpu":4}`, "4 vCPU and the tenant has 0 out of 2"},
		"a memory":                  {`{"name":"a","image":"debian","memoryMiB":8192}`, "8 GiB of memory and the tenant has 0 B out of 4 GiB"},
	} {
		e := newEnv(t)
		withTiny(e)
		e.networks.put("tiny", readyNetwork("net"))
		rec := e.do(t, "POST", "/v1/tenants/tiny/instances", "operator", tt.body)
		if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), strings.Split(tt.want, " and the tenant")[0]) {
			t.Errorf("%s: status %d: %s, want %q", name, rec.Code, rec.Body, tt.want)
		}
	}
}

func TestAThirdNetworkIsRefusedWithTheLimit(t *testing.T) {
	e := newEnv(t)
	withTiny(e)
	for _, n := range []string{"one", "two"} {
		if code := call(t, e, "POST", "/v1/tenants/tiny/networks", "operator", `{"name":"`+n+`"}`); code != http.StatusAccepted {
			t.Fatalf("network %s: status %d", n, code)
		}
	}
	rec := e.do(t, "POST", "/v1/tenants/tiny/networks", "operator", `{"name":"three"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), "2 networks out of 2 allowed: delete one first") {
		t.Errorf("the third: status %d: %s", rec.Code, rec.Body)
	}
	if len(e.networks.created) != 2 {
		t.Errorf("the refused network reached the store")
	}
}

func TestAThirdProfileIsRefusedWithTheLimit(t *testing.T) {
	e := newEnv(t)
	withTiny(e)
	const sz = `"cpu":1,"memoryMiB":1024,"diskGiB":10`
	for _, n := range []string{"one", "two"} {
		if code := call(t, e, "POST", "/v1/tenants/tiny/profiles", "operator", `{"name":"`+n+`",`+sz+`}`); code != http.StatusAccepted {
			t.Fatalf("profile %s: status %d", n, code)
		}
	}
	rec := e.do(t, "POST", "/v1/tenants/tiny/profiles", "operator", `{"name":"three",`+sz+`}`)
	if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), "2 profiles out of 2 allowed (s, m and l do not count): delete one first") {
		t.Errorf("the third: status %d: %s", rec.Code, rec.Body)
	}
	if len(e.profiles.created) != 2 {
		t.Errorf("the refused profile reached the store: %d created", len(e.profiles.created))
	}
	// s, m and l are not counted: the two above came next to them, and the profile of another tenant is not this one's
	if code := call(t, e, "POST", "/v1/tenants/demo/profiles", "operator", `{"name":"one",`+sz+`}`); code != http.StatusAccepted {
		t.Errorf("a profile of another tenant: status %d", code)
	}
}

func TestATenantHoldsAtMostSomeImages(t *testing.T) {
	s := testSettings
	s.MaxImagesPerTenant = 2
	e := newEnvWith(t, s)
	e.images.put("demo", readyImage("one"))
	if code := call(t, e, "POST", "/v1/tenants/demo/images", "operator", `{"name":"two","source":{"name":"debian/13/cloud"}}`); code != http.StatusAccepted {
		t.Fatalf("the second image: status %d", code)
	}
	rec := e.do(t, "POST", "/v1/tenants/demo/images", "operator", `{"name":"three","source":{"name":"debian/13/cloud"}}`)
	if rec.Code != http.StatusConflict || !strings.Contains(quotaCode(t, rec.Body.String()), "2 out of 2") {
		t.Errorf("the third: status %d: %s", rec.Code, rec.Body)
	}
	// 0 is no limit
	e = newEnv(t)
	for i := 0; i < 30; i++ {
		e.images.put("demo", readyImage("i"+strings.Repeat("x", i)))
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/images", "operator", `{"name":"more","source":{"name":"debian/13/cloud"}}`); code != http.StatusAccepted {
		t.Errorf("no limit: status %d", code)
	}
}

func TestACallerThatWritesTooMuchIsSlowedDown(t *testing.T) {
	s := testSettings
	s.RateLimitPerMinute, s.RateLimitBurst = 60, 3
	e := newEnvWith(t, s)
	for i := 0; i < 3; i++ {
		if code := call(t, e, "PATCH", "/v1/tenants/demo", "admin", `{"displayName":"x"}`); code == http.StatusTooManyRequests {
			t.Fatalf("request %d of the burst was limited", i+1)
		}
	}
	rec := e.do(t, "PATCH", "/v1/tenants/demo", "admin", `{"displayName":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 4th: status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1 second at a rate of one a second", got)
	}
	if !strings.Contains(rec.Body.String(), `"rate_limited"`) || !strings.Contains(rec.Body.String(), "try again in 1 seconds") {
		t.Errorf("body = %s", rec.Body)
	}
	// reading is not limited, another caller is not slowed down
	for i := 0; i < 20; i++ {
		if code := call(t, e, "GET", "/v1/tenants/demo", "admin", ""); code != http.StatusOK {
			t.Fatalf("a read was limited: %d", code)
		}
	}
	if code := call(t, e, "PATCH", "/v1/tenants/demo", "admin2", `{"displayName":"x"}`); code == http.StatusTooManyRequests {
		t.Error("another caller was slowed down")
	}
}

func TestWithoutALimitNothingIsSlowedDown(t *testing.T) {
	e := newEnv(t) // RateLimitPerMinute is 0
	for i := 0; i < 50; i++ {
		if code := call(t, e, "PATCH", "/v1/tenants/demo", "admin", `{"displayName":"x"}`); code == http.StatusTooManyRequests {
			t.Fatal("limited with no limit set")
		}
	}
}

func TestNotLimitedBeforeTheCredentialIsKnown(t *testing.T) {
	s := testSettings
	s.RateLimitPerMinute, s.RateLimitBurst = 60, 1
	e := newEnvWith(t, s)
	for i := 0; i < 5; i++ {
		if code := call(t, e, "POST", "/v1/tenants/demo/networks", "", `{"name":"x"}`); code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401 and never 429 for a caller nobody knows", code)
		}
	}
}
