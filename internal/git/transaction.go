package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

const InternalRefs = "refs/gitman/"

type RefChange struct{ Ref, Old, New string }

// ApplyOperation changes public refs, pins every retained commit and installs a
// receipt in one Git ref transaction. The receipt distinguishes an applied intent
// from an intent that never changed Git after the process or DB connection dies.
func (r *Repo) ApplyOperation(ctx context.Context, operationID string, changes []RefChange, commits []string, payload []byte) error {
	if !repoIDPattern.MatchString(operationID) {
		return fmt.Errorf("invalid operation ID")
	}
	opts := r.opts()
	opts.stdin = bytes.NewReader(payload)
	out, err := run(ctx, opts, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	receipt := strings.TrimSpace(string(out))
	if !IsHash(receipt) {
		return fmt.Errorf("invalid receipt hash")
	}
	var input strings.Builder
	input.WriteString("start\n")
	for _, c := range changes {
		kind, name, ok := SplitFullName(c.Ref)
		if !ok || ValidateName(name) != nil || FullName(kind, name) != c.Ref || !IsHash(c.Old) || !IsHash(c.New) {
			return fmt.Errorf("invalid ref change")
		}
		if IsZeroHash(c.New) {
			fmt.Fprintf(&input, "delete %s %s\n", c.Ref, c.Old)
		} else {
			fmt.Fprintf(&input, "update %s %s %s\n", c.Ref, c.New, c.Old)
		}
	}
	seen := map[string]bool{}
	for _, commit := range commits {
		if !IsHash(commit) || IsZeroHash(commit) {
			return fmt.Errorf("invalid retained commit")
		}
		if !seen[commit] {
			fmt.Fprintf(&input, "update %spins/%s %s\n", InternalRefs, commit, commit)
			seen[commit] = true
		}
	}
	fmt.Fprintf(&input, "create %soperations/%s %s\n", InternalRefs, operationID, receipt)
	input.WriteString("prepare\ncommit\n")
	opts = r.opts()
	opts.stdin = strings.NewReader(input.String())
	_, err = run(ctx, opts, "update-ref", "--stdin")
	return err
}

func (r *Repo) OperationApplied(ctx context.Context, operationID string, payload []byte) (bool, error) {
	if !repoIDPattern.MatchString(operationID) {
		return false, fmt.Errorf("invalid operation ID")
	}
	out, err := run(ctx, r.opts(), "rev-parse", "--verify", "--quiet", InternalRefs+"operations/"+operationID)
	if exitCode(err) == 1 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	opts := r.opts()
	opts.stdin = bytes.NewReader(payload)
	want, err := run(ctx, opts, "hash-object", "--stdin")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(want)) {
		return false, fmt.Errorf("operation receipt does not match durable intent")
	}
	return true, nil
}

// PinCommit makes manually selected historical commits survive ref deletion/GC.
func (r *Repo) PinCommit(ctx context.Context, commit string) error {
	if !IsHash(commit) || IsZeroHash(commit) {
		return fmt.Errorf("invalid retained commit")
	}
	if _, err := r.ResolveCommit(ctx, commit); err != nil {
		return err
	}
	_, err := run(ctx, r.opts(), "update-ref", InternalRefs+"pins/"+commit, commit)
	return err
}

func (r *Repo) CheckIntegrity(ctx context.Context) error {
	_, err := run(ctx, r.opts(), "fsck", "--full", "--no-dangling")
	return err
}

// ReconcilePins runs under the repository mutation lock. Pins precede GC;
// receipts remain while their operation is retained in PostgreSQL.
func (r *Repo) ReconcilePins(ctx context.Context, objects, operations []string) error {
	wanted := map[string]bool{}
	var input strings.Builder
	for _, hash := range objects {
		if !IsHash(hash) || IsZeroHash(hash) {
			continue
		}
		ref := InternalRefs + "pins/" + hash
		if !wanted[ref] {
			fmt.Fprintf(&input, "update %s %s\n", ref, hash)
			wanted[ref] = true
		}
	}
	for _, op := range operations {
		if !repoIDPattern.MatchString(op) {
			return fmt.Errorf("invalid retained operation")
		}
		wanted[InternalRefs+"operations/"+op] = true
	}
	out, err := run(ctx, r.opts(), "for-each-ref", "--format=%(refname)", InternalRefs)
	if err != nil {
		return err
	}
	for _, ref := range strings.Fields(string(out)) {
		if !wanted[ref] {
			fmt.Fprintf(&input, "delete %s\n", ref)
		}
	}
	if input.Len() > 0 {
		opts := r.opts()
		opts.stdin = strings.NewReader("start\n" + input.String() + "prepare\ncommit\n")
		if _, err := run(ctx, opts, "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	_, err = run(ctx, r.opts(), "gc", "--auto")
	return err
}
