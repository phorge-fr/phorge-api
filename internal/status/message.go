package status

import (
	"regexp"
	"strings"
)

// What a client is told when the platform could not do something yet. Nothing in these texts comes from Crossplane, a
// provider or Incus.
const (
	platformMessage = "The platform could not complete this step yet. It is retried; if it lasts, contact the platform administrators."
	imageMessage    = "The image could not be pulled from its server. Check its remote, name and architecture."
	instanceMessage = "The instance could not be created or changed."
	failedMessage   = "It has not become ready for several minutes. The platform administrators can see why."
)

// A refusal of Incus that the user caused and can act on, turned into what to do about it. The first pattern that matches an
// entry decides, so the specific ones come first.
var actionable = []struct {
	re  *regexp.Regexp
	say string
}{
	{regexp.MustCompile(`Reached maximum number of instances`), "This tenant has reached the maximum number of instances it is allowed."},
	// Incus says it either way: "Reached maximum number of networks" for a count, "Networks limit has been reached for project" for a
	// network (verified on the cluster, 2026-11-08).
	{regexp.MustCompile(`Reached maximum number of networks|Networks limit has been reached`), "This tenant has reached the maximum number of networks it is allowed."},
	{regexp.MustCompile(`Reached maximum aggregate value .*limits\.disk`), "This tenant has reached its disk limit."},
	{regexp.MustCompile(`Reached maximum aggregate value .*limits\.memory`), "This tenant has reached its memory limit."},
	{regexp.MustCompile(`Reached maximum aggregate value .*limits\.cpu`), "This tenant has reached its CPU limit."},
	{regexp.MustCompile(`Reached maximum`), "This tenant has reached one of its limits."},
	{regexp.MustCompile(`incompatible with secureboot`), "This image cannot start with secure boot: create the instance with secureBoot set to false."},
	{regexp.MustCompile(`in-use by`), "This image is used by an instance: delete the instance first."},
	{regexp.MustCompile(`prevent_destroy`), "This instance cannot be changed in place: delete it and create it again."},
}

// Public is status.problems as a client may see it. status.problems is a dump of the conditions of the composed resources
// ("network Synced: create failed: apply failed: Failed to create network ..."): it names Crossplane's objects, providers and
// Incus projects, which belong to the platform. A client gets instead:
//
//   - nothing for a resource that is merely not ready yet;
//   - a sentence for a refusal it can act on (a quota, a secure boot image);
//   - one sentence for an image or an instance that does not work, and one for everything the platform does behind them.
//
// Each sentence appears once. The raw text stays in status.problems of the object, for the administrators.
func Public(raw string) string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, entry := range strings.Split(raw, " | ") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		resource, msg := split(entry)
		if msg == "not ready" {
			continue
		}
		if say, ok := say(msg); ok {
			add(say)
			continue
		}
		switch resource {
		case "image":
			add(imageMessage)
		case "instance":
			add(instanceMessage)
		default:
			add(platformMessage)
		}
	}
	return strings.Join(out, " ")
}

// Message is what a client reads next to the phase: the public problems, or, for an object that failed without any, a
// sentence that says so.
func (o Object) Message(phase Phase) string {
	if m := Public(o.Problems); m != "" {
		return m
	}
	if phase == Failed {
		return failedMessage
	}
	return ""
}

// split reads "<resource> <Condition>: <message>" as the compositions write it.
func split(entry string) (resource, msg string) {
	head, msg, ok := strings.Cut(entry, ": ")
	if !ok {
		return "", entry
	}
	resource, _, _ = strings.Cut(head, " ")
	return resource, msg
}

func say(msg string) (string, bool) {
	for _, a := range actionable {
		if a.re.MatchString(msg) {
			return a.say, true
		}
	}
	return "", false
}
