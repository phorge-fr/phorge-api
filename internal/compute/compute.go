// Package compute is what the API knows about the Image and Instance kinds of a tenant: their fields, what a request may
// ask for and how strictly it is checked. The objects themselves are read and written by internal/kube.
package compute

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// Errors of the stores, the ones of every kind.
var (
	ErrNotFound = resource.ErrNotFound
	ErrExists   = resource.ErrExists
)

// InvalidError lists what is wrong with a request, field by field.
type InvalidError = resource.InvalidError

// InUseError is a refusal to delete an image that instances still start from.
type InUseError = resource.InUseError

var (
	// An Image is also a Kubernetes object name, an Instance is also an Incus instance name: a DNS label that starts with a
	// letter, which Incus requires.
	imageNameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	instanceNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	aliasRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	// The name of an image on its remote, e.g. debian/13/cloud or alpine/3.22.
	sourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]{0,127}$`)
)

// problems collects what is wrong and makes the error once.
type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &InvalidError{Problems: p}
}

// ValidImageName reports whether s can name an Image.
func ValidImageName(s string) bool { return imageNameRe.MatchString(s) }

// ValidInstanceName reports whether s can name an Instance.
func ValidInstanceName(s string) bool { return instanceNameRe.MatchString(s) }

// ---- Image ----

// Remotes are the image servers a tenant may pull from. The project cannot restrict them itself (the provider sends the
// server without a scheme and Incus then refuses every pull), so the Image kind is the only gate and this is it.
var Remotes = []string{"images"}

// DefaultRemote is the linuxcontainers.org server.
const DefaultRemote = "images"

// MaxAliases is how many aliases an image may have.
const MaxAliases = 10

// Image is an Image object as the API presents it. The source is always a virtual machine for the architecture of the
// compute nodes, which the XRD defaults.
type Image struct {
	Name        string
	Remote      string
	SourceName  string
	Aliases     []string
	Fingerprint string
	CreatedAt   time.Time
	CreatedBy   string
	Status      status.Object
}

// ImageSource says where an image comes from.
type ImageSource struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
}

// CreateImage is what creating an image asks for.
type CreateImage struct {
	Name    string      `json:"name"`
	Source  ImageSource `json:"source"`
	Aliases []string    `json:"aliases"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// Validate checks the request before anything is sent to the cluster.
func (r CreateImage) Validate() error {
	var p problems
	if !ValidImageName(r.Name) {
		p.add("name must be 1 to 63 characters: lowercase letters, digits and hyphens, not starting or ending with a hyphen")
	}
	if !sourceNameRe.MatchString(r.Source.Name) {
		p.add("source.name must be the name of an image on its server, for example debian/13/cloud")
	}
	if r.Source.Remote != "" && !slices.Contains(Remotes, r.Source.Remote) {
		p.add("source.remote must be one of %v", Remotes)
	}
	if len(r.Aliases) > MaxAliases {
		p.add("at most %d aliases", MaxAliases)
	}
	seen := map[string]bool{}
	for _, a := range r.Aliases {
		if !aliasRe.MatchString(a) {
			p.add("alias %q must be lowercase letters, digits, dots, underscores and hyphens", a)
		}
		if seen[a] {
			p.add("alias %q is given twice", a)
		}
		seen[a] = true
	}
	return p.err()
}

// ImageStore reads and writes the images of a tenant. Delete is in the foreground, like every deletion.
type ImageStore interface {
	List(ctx context.Context, tenant string) ([]Image, error)
	Get(ctx context.Context, tenant, name string) (Image, error)
	Create(ctx context.Context, tenant string, r CreateImage) (Image, error)
	// Delete returns an *InUseError when an instance still starts from the image.
	Delete(ctx context.Context, tenant, name string) error
}

// ---- Instance ----

// The bounds of an instance, which are the ones of the XRD. The plan of the tenant limits the total on top of them.
const (
	MinCPU, MaxCPU       = 1, 16
	MinMemory, MaxMemory = 256, 65536
	MinDisk, MaxDisk     = 1, 500
	MaxSSHKeys           = 20
)

// Instance is an Instance object as the API presents it.
type Instance struct {
	Name          string
	Image         string
	CPU           int
	MemoryMiB     int
	DiskGiB       int
	Running       bool
	Nesting       bool
	SecureBoot    bool
	SSHPublicKeys []string
	// State and IPv4 are what Incus reports, empty until the VM exists.
	State     string
	IPv4      string
	CreatedAt time.Time
	CreatedBy string
	Status    status.Object
}

// CreateInstance is what creating an instance asks for. What is not given is left to the defaults of the XRD.
type CreateInstance struct {
	Name          string   `json:"name"`
	Image         string   `json:"image"`
	CPU           *int     `json:"cpu"`
	MemoryMiB     *int     `json:"memoryMiB"`
	DiskGiB       *int     `json:"diskGiB"`
	Running       *bool    `json:"running"`
	Nesting       *bool    `json:"nesting"`
	SecureBoot    *bool    `json:"secureBoot"`
	SSHPublicKeys []string `json:"sshPublicKeys"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// PatchInstance is what can change on a running instance without recreating it (Incus applies cpu, memory, disk and the
// keys live, and start and stop keep the same VM). The image, nesting and secure boot cannot: delete and create.
type PatchInstance struct {
	Running       *bool     `json:"running"`
	CPU           *int      `json:"cpu"`
	MemoryMiB     *int      `json:"memoryMiB"`
	DiskGiB       *int      `json:"diskGiB"`
	SSHPublicKeys *[]string `json:"sshPublicKeys"`
}

func checkBounds(p *problems, name string, v *int, lo, hi int) {
	if v != nil && (*v < lo || *v > hi) {
		p.add("%s must be between %d and %d", name, lo, hi)
	}
}

// Validate checks the request.
func (r CreateInstance) Validate() error {
	var p problems
	if !ValidInstanceName(r.Name) {
		p.add("name must be 1 to 63 characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen")
	}
	if !ValidImageName(r.Image) {
		p.add("image must be the name of an image of the tenant")
	}
	checkBounds(&p, "cpu", r.CPU, MinCPU, MaxCPU)
	checkBounds(&p, "memoryMiB", r.MemoryMiB, MinMemory, MaxMemory)
	checkBounds(&p, "diskGiB", r.DiskGiB, MinDisk, MaxDisk)
	checkKeys(&p, r.SSHPublicKeys)
	return p.err()
}

// Validate checks the patch.
func (r PatchInstance) Validate() error {
	var p problems
	if r.Running == nil && r.CPU == nil && r.MemoryMiB == nil && r.DiskGiB == nil && r.SSHPublicKeys == nil {
		p.add("nothing to change: give running, cpu, memoryMiB, diskGiB or sshPublicKeys")
	}
	checkBounds(&p, "cpu", r.CPU, MinCPU, MaxCPU)
	checkBounds(&p, "memoryMiB", r.MemoryMiB, MinMemory, MaxMemory)
	checkBounds(&p, "diskGiB", r.DiskGiB, MinDisk, MaxDisk)
	if r.SSHPublicKeys != nil {
		checkKeys(&p, *r.SSHPublicKeys)
	}
	return p.err()
}

// sshKeyRe is the shape of a line of authorized_keys that is accepted: a key type, the key in base64 and, at most, a
// comment that is one word. It is strict on purpose. The composition writes the keys into the cloud-init of the VM with a
// plain printf, one "  - <key>" per line, and nothing else protects it: a key with a line break would add a line of
// cloud-init (a command run as root in the VM), and a comment holding ": " or " #" would change how the line is read.
var sshKeyRe = regexp.MustCompile(`\A(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]{16,}={0,3}( [A-Za-z0-9@._+=/-]{1,100})?\z`)

// ValidSSHKey reports whether key can be given to an instance.
func ValidSSHKey(key string) bool { return len(key) <= maxSSHKeyLength && sshKeyRe.MatchString(key) }

// maxSSHKeyLength is generous: a 4096 bits RSA key is about 750 characters.
const maxSSHKeyLength = 4096

func checkKeys(p *problems, keys []string) {
	if len(keys) > MaxSSHKeys {
		p.add("at most %d sshPublicKeys", MaxSSHKeys)
	}
	seen := map[string]bool{}
	for i, k := range keys {
		if !ValidSSHKey(k) {
			p.add("sshPublicKeys[%d] must be one line, `<type> <base64 key>` and at most a one word comment of letters, digits and @._+=/-", i)
		}
		if seen[k] {
			p.add("sshPublicKeys[%d] is given twice", i)
		}
		seen[k] = true
	}
}

// InstanceStore reads and writes the instances of a tenant.
type InstanceStore interface {
	List(ctx context.Context, tenant string) ([]Instance, error)
	Get(ctx context.Context, tenant, name string) (Instance, error)
	Create(ctx context.Context, tenant string, r CreateInstance) (Instance, error)
	Patch(ctx context.Context, tenant, name string, r PatchInstance) (Instance, error)
	Delete(ctx context.Context, tenant, name string) error
}
