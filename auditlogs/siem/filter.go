package siem

import (
	"fmt"
	"regexp"
	"strings"
)

// eventTypePattern matches every filter entry
var eventTypePattern = regexp.MustCompile(`^[a-z_]+\.([a-z_]+|\*)$`)

// filter decides which event types are shipped
// An empty filter matches everything
type filter struct {
	exact map[string]struct{}
	areas map[string]struct{}
}

// newFilter compiles a list of filter entries.
//
// Each entry is either an exact event type ("account.login") or an area wildcard ("account.*").
// When known is non-empty, every entry is also checked against it, so a typo fails at startup rather than silently dropping every event.
func newFilter(entries []string, known []string) (filter, error) {
	f := filter{}
	if len(entries) == 0 {
		return f, nil
	}

	knownSet := make(map[string]struct{}, len(known))
	knownAreas := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
		area, _, ok := strings.Cut(k, ".")
		if ok {
			knownAreas[area] = struct{}{}
		}
	}

	f.exact = make(map[string]struct{}, len(entries))
	f.areas = make(map[string]struct{}, len(entries))

	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}

		if !eventTypePattern.MatchString(entry) {
			return filter{}, fmt.Errorf(`invalid event type filter %q: expected the form "area.verb" or "area.*"`, raw)
		}

		area, verb, _ := strings.Cut(entry, ".")
		if verb == "*" {
			if len(knownAreas) > 0 {
				_, ok := knownAreas[area]
				if !ok {
					return filter{}, fmt.Errorf("invalid event type filter %q: no known event type belongs to area %q", raw, area)
				}
			}

			f.areas[area] = struct{}{}
			continue
		}

		if len(knownSet) > 0 {
			_, ok := knownSet[entry]
			if !ok {
				return filter{}, fmt.Errorf("invalid event type filter %q: not a known event type", raw)
			}
		}

		f.exact[entry] = struct{}{}
	}

	return f, nil
}

// matchAll reports whether the filter lets every event through
func (f filter) matchAll() bool {
	return len(f.exact) == 0 && len(f.areas) == 0
}

// matches reports whether an event type passes the filter
func (f filter) matches(eventType string) bool {
	if f.matchAll() {
		return true
	}

	_, ok := f.exact[eventType]
	if ok {
		return true
	}

	area, _, ok := strings.Cut(eventType, ".")
	if !ok {
		return false
	}

	_, ok = f.areas[area]
	return ok
}
