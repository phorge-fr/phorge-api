package auth

import "regexp"

var (
	tenantRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,27}$`)
	// idRe is the id of a key, which is the name of its ApiKey object: 8 random bytes in hex.
	idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// ValidTenantName reports whether s has the shape of a tenant slug. Anything that becomes part of an OpenFGA object
// ("tenant:<name>") or of a namespace must pass it first.
func ValidTenantName(s string) bool { return tenantRe.MatchString(s) }

// ValidKeyID reports whether s has the shape of the id of a key.
func ValidKeyID(s string) bool { return idRe.MatchString(s) }
