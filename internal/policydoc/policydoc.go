// Package policydoc parses IAM policy JSON, which is loose about shapes:
// Statement, Action, Resource and Principal may each be a string, a list or an object.
package policydoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// StringList accepts either "x" or ["x","y"].
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var l []string
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		*s = l
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = []string{one}
	return nil
}

// Principal maps a principal type (AWS, Service, Federated, CanonicalUser) to values.
type Principal map[string][]string

type Statement struct {
	Sid          string
	Effect       string
	Actions      []string
	NotActions   []string
	Resources    []string
	NotResources []string
	Principal    Principal
	HasCondition bool
	Condition    map[string]map[string]StringList
}

type Document struct {
	Version    string
	Statements []Statement
}

type rawStatement struct {
	Sid          string          `json:"Sid"`
	Effect       string          `json:"Effect"`
	Action       StringList      `json:"Action"`
	NotAction    StringList      `json:"NotAction"`
	Resource     StringList      `json:"Resource"`
	NotResource  StringList      `json:"NotResource"`
	Principal    json.RawMessage `json:"Principal"`
	NotPrincipal json.RawMessage `json:"NotPrincipal"`
	Condition    json.RawMessage `json:"Condition"`
}

// Parse decodes a policy document.
func Parse(raw []byte) (*Document, error) {
	var d struct {
		Version   string          `json:"Version"`
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("policy document: %w", err)
	}
	var rs []rawStatement
	st := bytes.TrimSpace(d.Statement)
	switch {
	case len(st) == 0 || string(st) == "null":
	case st[0] == '[':
		if err := json.Unmarshal(st, &rs); err != nil {
			return nil, fmt.Errorf("policy statements: %w", err)
		}
	default:
		var one rawStatement
		if err := json.Unmarshal(st, &one); err != nil {
			return nil, fmt.Errorf("policy statement: %w", err)
		}
		rs = []rawStatement{one}
	}
	doc := &Document{Version: d.Version}
	for _, r := range rs {
		p, err := parsePrincipal(r.Principal)
		if err != nil {
			return nil, err
		}
		s := Statement{
			Sid: r.Sid, Effect: r.Effect, Actions: r.Action, NotActions: r.NotAction,
			Resources: r.Resource, NotResources: r.NotResource, Principal: p,
		}
		if c := bytes.TrimSpace(r.Condition); len(c) > 0 && string(c) != "null" && string(c) != "{}" {
			s.HasCondition = true
			// Condition shape is best-effort; an unparsable one still counts as present.
			_ = json.Unmarshal(c, &s.Condition)
		}
		doc.Statements = append(doc.Statements, s)
	}
	return doc, nil
}

func parsePrincipal(b json.RawMessage) (Principal, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		return Principal{"AWS": {s}}, nil
	}
	m := map[string]StringList{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("policy principal: %w", err)
	}
	p := Principal{}
	for k, v := range m {
		p[k] = v
	}
	return p, nil
}

// Allow reports whether the statement has Effect Allow.
func (s Statement) Allow() bool { return strings.EqualFold(s.Effect, "Allow") }

// MatchesAction reports whether any Action pattern matches the given action,
// honouring IAM wildcards and case-insensitivity.
func (s Statement) MatchesAction(action string) bool {
	return anyMatch(s.Actions, action)
}

// AllResources reports whether the statement applies to Resource "*".
func (s Statement) AllResources() bool {
	for _, r := range s.Resources {
		if r == "*" {
			return true
		}
	}
	return false
}

func anyMatch(patterns []string, action string) bool {
	a := strings.ToLower(action)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), a); ok {
			return true
		}
	}
	return false
}
