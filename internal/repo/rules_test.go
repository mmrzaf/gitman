package repo

import (
	"testing"

	"github.com/mmrzaf/gitman/internal/git"
)

func TestEvaluateNoMatchingRuleFollowsDefaultPush(t *testing.T) {
	everyone := Evaluate(nil, git.KindBranch, "feature/x", "person-1", false, PushEveryone, nil)
	if !everyone.CanPush || !everyone.AllowForce || !everyone.AllowDelete {
		t.Errorf("Evaluate = %+v, want push, force and delete allowed under a default of everyone", everyone)
	}
	if everyone.AllowDocker || everyone.AllowSecrets || everyone.AllowDeploy || everyone.RunOnPush {
		t.Error("expected no CI capability to be granted when no rule matches")
	}
	if everyone.MatchedRule != nil {
		t.Error("expected no matched rule")
	}

	admins := Evaluate(nil, git.KindBranch, "feature/x", "person-1", false, PushAdmins, nil)
	if admins.CanPush {
		t.Error("expected a non-admin to be denied under a default of admins")
	}
	if !admins.AllowForce || !admins.AllowDelete {
		t.Error("expected force and delete to stay allowed on an unmatched ref regardless of the default push policy")
	}
	adminAdmin := Evaluate(nil, git.KindBranch, "feature/x", "person-1", true, PushAdmins, nil)
	if !adminAdmin.CanPush {
		t.Error("expected an admin to be allowed under a default of admins")
	}

	people := Evaluate(nil, git.KindBranch, "feature/x", "person-1", false, PushPeople, []string{"person-2"})
	if people.CanPush {
		t.Error("expected a person not on the default push list to be denied")
	}
	listed := Evaluate(nil, git.KindBranch, "feature/x", "person-2", false, PushPeople, []string{"person-2"})
	if !listed.CanPush {
		t.Error("expected a person on the default push list to be allowed")
	}
}

func TestEvaluateMatchedRuleProtectsForceAndDelete(t *testing.T) {
	rules := []Rule{{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushEveryone}}
	d := Evaluate(rules, git.KindBranch, "main", "person-1", false, PushEveryone, nil)
	if !d.CanPush {
		t.Error("expected push to be allowed")
	}
	if d.AllowForce || d.AllowDelete {
		t.Error("expected force and delete to follow the rule, which allows neither")
	}
}

func TestEvaluateEveryonePolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindBranch, Pattern: "develop", PushPolicy: PushEveryone, RunOnPush: true, AllowDocker: true}}
	d := Evaluate(rules, git.KindBranch, "develop", "person-1", false, PushEveryone, nil)
	if !d.CanPush || !d.RunOnPush || !d.AllowDocker {
		t.Errorf("Evaluate = %+v, want push+run+docker all true", d)
	}
}

func TestEvaluateAdminsPolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushAdmins, AllowDeploy: true}}

	member := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false, PushEveryone, nil)
	if member.CanPush {
		t.Error("expected a non-admin to be denied under PushAdmins")
	}

	admin := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", true, PushEveryone, nil)
	if !admin.CanPush {
		t.Error("expected an admin to be allowed under PushAdmins")
	}
}

func TestEvaluatePeoplePolicy(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushPeople, PushPeople: []string{"person-2"}}}

	denied := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false, PushEveryone, nil)
	if denied.CanPush {
		t.Error("expected a person not on the list to be denied")
	}
	allowed := Evaluate(rules, git.KindTag, "v1.4.2", "person-2", false, PushEveryone, nil)
	if !allowed.CanPush {
		t.Error("expected a person on the list to be allowed")
	}
	adminOverride := Evaluate(rules, git.KindTag, "v1.4.2", "person-3", true, PushEveryone, nil)
	if !adminOverride.CanPush {
		t.Error("expected an admin to be allowed even when not on the list")
	}
}

func TestEvaluateExactRuleBeatsWildcard(t *testing.T) {
	rules := []Rule{
		{Kind: git.KindBranch, Pattern: "*", PushPolicy: PushEveryone},
		{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushAdmins},
	}
	d := Evaluate(rules, git.KindBranch, "main", "person-1", false, PushEveryone, nil)
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
	d := Evaluate(rules, git.KindTag, "v1.4.2", "person-1", false, PushEveryone, nil)
	if d.MatchedRule == nil || d.MatchedRule.Pattern != "v*" {
		t.Errorf("MatchedRule = %v, want the more specific 'v*' rule", d.MatchedRule)
	}
	if d.CanPush {
		t.Error("expected the more specific rule's policy (admins only) to apply")
	}
}

func TestEvaluateKindIsolation(t *testing.T) {
	rules := []Rule{{Kind: git.KindTag, Pattern: "*", PushPolicy: PushAdmins}}
	d := Evaluate(rules, git.KindBranch, "develop", "person-1", false, PushEveryone, nil)
	if !d.CanPush {
		t.Error("a tag rule must not restrict a branch under the same name or pattern")
	}
}

func TestValidateDefaultPush(t *testing.T) {
	valid := []struct {
		policy PushPolicy
		people []string
	}{
		{PushEveryone, nil},
		{PushAdmins, nil},
		{PushPeople, []string{"person-1"}},
	}
	for _, c := range valid {
		if err := ValidateDefaultPush(c.policy, c.people); err != nil {
			t.Errorf("ValidateDefaultPush(%q, %v): %v", c.policy, c.people, err)
		}
	}
	invalid := []struct {
		policy PushPolicy
		people []string
	}{
		{PushEveryone, []string{"person-1"}},
		{PushPeople, nil},
		{"nobody", nil},
	}
	for _, c := range invalid {
		if err := ValidateDefaultPush(c.policy, c.people); err == nil {
			t.Errorf("ValidateDefaultPush(%q, %v): expected an error", c.policy, c.people)
		}
	}
}

func TestCheckDelete(t *testing.T) {
	alice := Who{ID: "alice-id", Username: "alice"}
	admin := Who{ID: "admin-id", Username: "root", IsAdmin: true}
	repoOf := func(policy PushPolicy, people ...string) *Repo {
		return &Repo{DefaultBranch: "main", DefaultPushPolicy: policy, DefaultPushPeople: people}
	}
	rules := []Rule{
		{Kind: git.KindBranch, Pattern: "main", PushPolicy: PushEveryone, AllowDelete: true},
		{Kind: git.KindBranch, Pattern: "release/*", PushPolicy: PushEveryone},
		{Kind: git.KindBranch, Pattern: "ops/*", PushPolicy: PushAdmins, AllowDelete: true},
		{Kind: git.KindTag, Pattern: "v*", PushPolicy: PushPeople, PushPeople: []string{"alice-id"}, AllowDelete: true},
		{Kind: git.KindTag, Pattern: "keep-*", PushPolicy: PushEveryone},
	}

	for _, tc := range []struct {
		name string
		repo *Repo
		kind git.Kind
		ref  string
		who  Who
		// want is the refusal, or "" when the deletion is allowed.
		want string
	}{
		{"a ref no rule matches, pushed to by everyone", repoOf(PushEveryone), git.KindBranch, "feature", alice, ""},
		{"a tag no rule matches", repoOf(PushEveryone), git.KindTag, "old", alice, ""},
		{"the default branch, even when its rule allows deleting", repoOf(PushEveryone), git.KindBranch, "main", alice,
			"the default branch cannot be deleted"},
		{"the default branch, for an admin", repoOf(PushEveryone), git.KindBranch, "main", admin,
			"the default branch cannot be deleted"},
		{"a tag named like the default branch", repoOf(PushEveryone), git.KindTag, "main", alice, ""},
		{"a branch whose rule does not allow deleting", repoOf(PushEveryone), git.KindBranch, "release/1.0", alice,
			`the rule for branch "release/*" does not allow deleting it`},
		{"a branch whose rule does not allow deleting, for an admin", repoOf(PushEveryone), git.KindBranch, "release/1.0", admin,
			`the rule for branch "release/*" does not allow deleting it`},
		{"a tag whose rule does not allow deleting", repoOf(PushEveryone), git.KindTag, "keep-me", alice,
			`the rule for tag "keep-*" does not allow deleting it`},
		{"a ref the person may not push to", repoOf(PushEveryone), git.KindBranch, "ops/deploy", alice,
			`the rule for branch "ops/*" does not allow alice to push here`},
		{"a ref only admins push to, for an admin", repoOf(PushEveryone), git.KindBranch, "ops/deploy", admin, ""},
		{"a tag only named people push to, for one of them", repoOf(PushEveryone), git.KindTag, "v1", alice, ""},
		{"a tag only named people push to, for someone else", repoOf(PushEveryone), git.KindTag, "v1", Who{ID: "bob-id", Username: "bob"},
			`the rule for tag "v*" does not allow bob to push here`},
		{"an unmatched ref under a default push policy of admins", repoOf(PushAdmins), git.KindBranch, "feature", alice,
			"no rule does not allow alice to push here"},
		{"an unmatched ref under a default push policy of admins, for an admin", repoOf(PushAdmins), git.KindBranch, "feature", admin, ""},
		{"an unmatched ref under a default push policy naming the person", repoOf(PushPeople, "alice-id"), git.KindBranch, "feature", alice, ""},
		// Not being allowed to push is said before the default branch is.
		{"the default branch, for someone who may not push", &Repo{DefaultBranch: "ops/main", DefaultPushPolicy: PushEveryone},
			git.KindBranch, "ops/main", alice, `the rule for branch "ops/*" does not allow alice to push here`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, reason := CheckDelete(tc.repo, rules, tc.kind, tc.ref, tc.who)
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
			want := Evaluate(rules, tc.kind, tc.ref, tc.who.ID, tc.who.IsAdmin, tc.repo.DefaultPushPolicy, tc.repo.DefaultPushPeople)
			if d.CanPush != want.CanPush || d.AllowDelete != want.AllowDelete || d.MatchedRule != want.MatchedRule {
				t.Errorf("decision = %+v, want %+v", d, want)
			}
		})
	}
}
