package repo

import (
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestEvaluateNoMatchingRuleLeavesRefUnprotected(t *testing.T) {
	d := Evaluate(nil, git.KindBranch, "feature/x", "person-1", false)
	if !d.CanPush || !d.AllowForce || !d.AllowDelete {
		t.Errorf("Evaluate = %+v, want push, force and delete allowed on an unprotected ref", d)
	}
	if d.AllowDocker || d.AllowSecrets || d.AllowShip || d.RunOnPush {
		t.Error("expected no CI capability to be granted when no rule matches")
	}
	if d.MatchedRule != nil {
		t.Error("expected no matched rule")
	}
}

func TestEvaluateMatchedRuleProtectsForceAndDelete(t *testing.T) {
	rules := []Rule{{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushEveryone}}
	d := Evaluate(rules, git.KindBranch, "main", "person-1", false)
	if !d.CanPush {
		t.Error("expected push to be allowed")
	}
	if d.AllowForce || d.AllowDelete {
		t.Error("expected force and delete to follow the rule, which allows neither")
	}
}

func TestEvaluateEveryonePolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindBranch, Pattern: "develop", PushPolicy: PushEveryone, RunOnPush: true, AllowDocker: true}}
	d := Evaluate(rules, git.KindBranch, "develop", "person-1", false)
	if !d.CanPush || !d.RunOnPush || !d.AllowDocker {
		t.Errorf("Evaluate = %+v, want push+run+docker all true", d)
	}
}

func TestEvaluateAdminsPolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushAdmins, AllowShip: true}}

	member := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false)
	if member.CanPush {
		t.Error("expected a non-admin to be denied under PushAdmins")
	}

	admin := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", true)
	if !admin.CanPush {
		t.Error("expected an admin to be allowed under PushAdmins")
	}
}

func TestEvaluatePeoplePolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushPeople, PushPeople: []string{"person-2"}}}

	denied := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false)
	if denied.CanPush {
		t.Error("expected a person not on the list to be denied")
	}
	allowed := Evaluate(rules, git.KindTag, "v1.4.2", "person-2", false)
	if !allowed.CanPush {
		t.Error("expected a person on the list to be allowed")
	}
	adminOverride := Evaluate(rules, git.KindTag, "v1.4.2", "person-3", true)
	if !adminOverride.CanPush {
		t.Error("expected an admin to be allowed even when not on the list")
	}
}

func TestEvaluateExactRuleBeatsWildcard(t *testing.T) {
	rules := []Rule{
		{Kind: git.KindBranch, Pattern: "*", PushPolicy: PushEveryone},
		{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushAdmins},
	}
	d := Evaluate(rules, git.KindBranch, "main", "person-1", false)
	if d.CanPush {
		t.Error("expected the exact 'main' rule (admins only) to win over the wildcard rule")
	}
	if d.MatchedRule == nil || d.MatchedRule.Pattern != "main" {
		t.Errorf("MatchedRule = %v, want the exact 'main' rule", d.MatchedRule)
	}
}

func TestEvaluateMostSpecificWildcardWins(t *testing.T) {
	rules := []Rule{
		{Kind: git.KindTag, Pattern: "*", PushPolicy: PushEveryone},
		{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushAdmins},
	}
	d := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false)
	if d.MatchedRule == nil || d.MatchedRule.Pattern != "v*" {
		t.Errorf("MatchedRule = %v, want the more specific 'v*' rule", d.MatchedRule)
	}
	if d.CanPush {
		t.Error("expected the more specific rule's policy (admins only) to apply")
	}
}

func TestEvaluateKindIsolation(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "*", PushPolicy: PushAdmins}}
	d := Evaluate(rules, git.KindBranch, "develop", "person-1", false)
	if !d.CanPush {
		t.Error("a tag rule must not restrict a branch under the same name or pattern")
	}
}
