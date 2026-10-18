package analyze

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/saluja-ji/iam-inventory/internal/model"
)

// Change lists identities by ARN.
type Change struct {
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

func (c Change) Total() int { return len(c.Added) + len(c.Removed) + len(c.Modified) }

type Diff struct {
	Baseline bool   `json:"baseline"` // true when there was no earlier snapshot to compare with
	Previous string `json:"previous_run_id,omitempty"`
	Current  string `json:"current_run_id"`
	Roles    Change `json:"roles"`
	Users    Change `json:"users"`
	Groups   Change `json:"groups"`
	Policies Change `json:"policies"`
}

func (d Diff) Total() int {
	return d.Roles.Total() + d.Users.Total() + d.Groups.Total() + d.Policies.Total()
}

// Compare returns what changed between prev and cur. A nil prev yields a baseline diff.
func Compare(prev, cur *model.Snapshot) Diff {
	d := Diff{Current: cur.RunID}
	if prev == nil {
		d.Baseline = true
		return d
	}
	d.Previous = prev.RunID
	d.Roles = change(index(prev.Roles, func(r model.Role) (string, any) { r.LastUsed = nil; return r.ARN, r }),
		index(cur.Roles, func(r model.Role) (string, any) { r.LastUsed = nil; return r.ARN, r }))
	// Access keys are part of the user, so key rotation shows up as a user modification.
	d.Users = change(index(prev.Users, func(u model.User) (string, any) { return u.ARN, u }),
		index(cur.Users, func(u model.User) (string, any) { return u.ARN, u }))
	d.Groups = change(index(prev.Groups, func(g model.Group) (string, any) { return g.ARN, g }),
		index(cur.Groups, func(g model.Group) (string, any) { return g.ARN, g }))
	d.Policies = change(index(prev.Policies, func(p model.Policy) (string, any) {
		// attachment counts churn without the policy changing
		p.AttachmentCount, p.BoundaryUsage = 0, 0
		return p.ARN, p
	}), index(cur.Policies, func(p model.Policy) (string, any) {
		p.AttachmentCount, p.BoundaryUsage = 0, 0
		return p.ARN, p
	}))
	return d
}

func index[T any](items []T, key func(T) (string, any)) map[string]string {
	m := make(map[string]string, len(items))
	for _, it := range items {
		k, v := key(it)
		b, _ := json.Marshal(v)
		sum := sha256.Sum256(b)
		m[k] = hex.EncodeToString(sum[:])
	}
	return m
}

func change(prev, cur map[string]string) Change {
	c := Change{Added: []string{}, Removed: []string{}, Modified: []string{}}
	for k, h := range cur {
		old, ok := prev[k]
		switch {
		case !ok:
			c.Added = append(c.Added, k)
		case old != h:
			c.Modified = append(c.Modified, k)
		}
	}
	for k := range prev {
		if _, ok := cur[k]; !ok {
			c.Removed = append(c.Removed, k)
		}
	}
	sort.Strings(c.Added)
	sort.Strings(c.Removed)
	sort.Strings(c.Modified)
	return c
}
