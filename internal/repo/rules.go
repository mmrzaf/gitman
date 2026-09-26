package repo

import (
	"fmt"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/git"
)

// PushPolicy controls who may push to a ref matched by a rule.
type PushPolicy string

const (
	PushEveryone PushPolicy = "everyone"
	PushAdmins   PushPolicy = "admins"
	PushPeople   PushPolicy = "people"
)

// Rule mirrors one row of ref_rules.
type Rule struct {
	Kind         git.Kind
	Pattern      string
	PushPolicy   PushPolicy
	PushPeople   []string // person IDs; used only when PushPolicy == PushPeople
	AllowForce   bool
	AllowDelete  bool
	RunOnPush    bool
	AllowDocker  bool
	AllowSecrets bool
	AllowShip    bool
}

// Decision is what a rule set resolves to for one ref and one person. When
// no rule matches, the ref is unprotected: anyone may push to it,
// force-push it or delete it, and pushes to it do not run pipelines. A
// brand-new repository is fully usable immediately; rules only ever add
// protection or grant CI capabilities.
type Decision struct {
	CanPush      bool
	AllowForce   bool
	AllowDelete  bool
	RunOnPush    bool
	AllowDocker  bool
	AllowSecrets bool
	AllowShip    bool
	MatchedRule  *Rule
}

// Evaluate resolves the rule that applies to kind/name for one person.
// isAdmin overrides PushAdmins and PushPeople: an admin may always push,
// as the safety valve that lets an admin fix a misconfigured rule.
func Evaluate(rules []Rule, kind git.Kind, name string, personID string, isAdmin bool) Decision {
	matched := selectRule(rules, kind, name)
	if matched == nil {
		return Decision{CanPush: true, AllowForce: true, AllowDelete: true}
	}

	d := Decision{
		AllowForce:   matched.AllowForce,
		AllowDelete:  matched.AllowDelete,
		RunOnPush:    matched.RunOnPush,
		AllowDocker:  matched.AllowDocker,
		AllowSecrets: matched.AllowSecrets,
		AllowShip:    matched.AllowShip,
		MatchedRule:  matched,
	}
	switch matched.PushPolicy {
	case PushEveryone:
		d.CanPush = true
	case PushAdmins:
		d.CanPush = isAdmin
	case PushPeople:
		d.CanPush = isAdmin || containsID(matched.PushPeople, personID)
	}
	return d
}

// selectRule finds the single most specific rule matching kind/name,
// using git.SelectPattern so ref rules and pipeline targets resolve
// overlapping patterns by exactly the same precedence.
func selectRule(rules []Rule, kind git.Kind, name string) *Rule {
	var patterns []string
	var indices []int
	for i, rule := range rules {
		if rule.Kind != kind {
			continue
		}
		patterns = append(patterns, rule.Pattern)
		indices = append(indices, i)
	}
	best := git.SelectPattern(patterns, name)
	if best < 0 {
		return nil
	}
	return &rules[indices[best]]
}

func containsID(ids []string, id string) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

// ValidateRule checks a rule before it is saved.
func ValidateRule(r Rule) error {
	if r.Kind != git.KindBranch && r.Kind != git.KindTag {
		return apperr.New(apperr.KindInvalid, "rule kind must be branch or tag")
	}
	if err := git.ValidatePattern(r.Pattern); err != nil {
		return err
	}
	switch r.PushPolicy {
	case PushEveryone, PushAdmins:
		if len(r.PushPeople) > 0 {
			return apperr.New(apperr.KindInvalid, fmt.Sprintf("people can only be listed when the push policy is %q", PushPeople))
		}
	case PushPeople:
		if len(r.PushPeople) == 0 {
			return apperr.New(apperr.KindInvalid, fmt.Sprintf("a %q push policy must list at least one person", PushPeople))
		}
	default:
		return apperr.New(apperr.KindInvalid, fmt.Sprintf("unknown push policy %q", r.PushPolicy))
	}
	return nil
}
