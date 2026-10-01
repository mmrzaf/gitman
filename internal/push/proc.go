package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/repo"
)

// pushIntent snapshots authorization and scheduling, rather than consulting
// mutable rules on recovery. The stored payload is also hashed by Git's receipt.
type pushIntent struct {
	Repository *repo.Repo
	PersonID   string
	RemoteAddr string
	Records    []updateRecord
}

func readPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(string(header[:]), 16, 16)
	if err != nil || n > 65520 || n > 0 && n < 4 {
		return nil, fmt.Errorf("invalid proc-receive packet")
	}
	if n == 0 {
		return nil, nil
	}
	b := make([]byte, int(n)-4)
	_, err = io.ReadFull(r, b)
	return b, err
}

// ProcReceive speaks Git's pkt-line hook protocol. All commands go through this
// hook: intent commits before one atomic Git transaction, then DB rows and the
// operation completion receipt commit together.
func (h *Hook) ProcReceive(ctx context.Context, in io.Reader, out io.Writer) error {
	greeting, err := readPacket(in)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(greeting), "version=1") {
		return fmt.Errorf("unsupported proc-receive protocol")
	}
	if b, err := readPacket(in); err != nil || b != nil {
		return fmt.Errorf("invalid proc-receive negotiation")
	}
	if _, err := io.WriteString(out, git.PktLine("version=1\x00atomic")+"0000"); err != nil {
		return err
	}
	var input strings.Builder
	for count := 0; ; count++ {
		b, err := readPacket(in)
		if err != nil {
			return err
		}
		if b == nil {
			break
		}
		if count == maxUpdates {
			return fmt.Errorf("too many ref updates")
		}
		input.WriteString(strings.TrimSuffix(string(b), "\n"))
		input.WriteByte('\n')
	}
	updates, err := ParseUpdates(strings.NewReader(input.String()))
	if err != nil {
		return err
	}
	err = h.ApplyPush(ctx, updates)
	for _, u := range updates {
		result := "ok " + u.Ref
		if err != nil {
			result = "ng " + u.Ref + " Gitman could not complete this push"
			if errors.Is(err, git.ErrRefMoved) {
				result = "ng " + u.Ref + " ref changed; fetch before retrying"
			}
		}
		if _, writeErr := io.WriteString(out, git.PktLine(result+"\n")); writeErr != nil {
			return writeErr
		}
	}
	if _, writeErr := io.WriteString(out, "0000"); writeErr != nil {
		return writeErr
	}
	if err != nil {
		h.say("Gitman: %v", err)
	}
	// Results already describe rejection. A protocol failure, rather than a
	// domain rejection, is the reason to terminate the protocol unsuccessfully.
	return nil
}

func (h *Hook) ApplyPush(ctx context.Context, updates []Update) error {
	if len(updates) == 0 {
		return nil
	}
	return h.Repos.WithMutation(ctx, h.Ctx.RepoID, func() error {
		changes := make([]git.RefChange, len(updates))
		for i, u := range updates {
			changes[i] = git.RefChange{Ref: u.Ref, Old: u.Old, New: u.New}
		}
		if err := h.ValidatePush(ctx, updates); err != nil {
			return err
		}
		if err := h.Git.CheckRefChanges(ctx, changes); err != nil {
			return err
		}
		pc, err := h.load(ctx)
		if err != nil {
			return err
		}
		records, err := h.prepareRecords(ctx, pc, updates)
		if err != nil {
			return err
		}
		intent := pushIntent{Repository: pc.repo, PersonID: pc.person.ID, RemoteAddr: h.Ctx.RemoteAddr, Records: records}
		op, err := h.Repos.BeginOperation(ctx, pc.repo.ID, "push", "", pc.person.ID, intent)
		if err != nil {
			return err
		}
		applied, err := h.recoverPush(ctx, *op)
		if err != nil {
			return err
		}
		if !applied {
			return git.ErrRefMoved
		}
		return nil
	})
}

// RecoverPush requires the repository mutation lock. Replaying a receipt never
// creates another push or run, and never executes a pipeline itself.
func (h *Hook) RecoverPush(ctx context.Context, op repo.Operation) error {
	_, err := h.recoverPush(ctx, op)
	return err
}

func (h *Hook) recoverPush(ctx context.Context, op repo.Operation) (bool, error) {
	var intent pushIntent
	if err := json.Unmarshal(op.Payload, &intent); err != nil {
		return false, err
	}
	if intent.Repository == nil || intent.Repository.ID != op.RepoID {
		return false, fmt.Errorf("invalid push intent repository")
	}
	applied, err := h.Git.OperationApplied(ctx, op.ID, op.Payload)
	if err != nil {
		return false, err
	}
	if !applied {
		var changes []git.RefChange
		var commits []string
		for _, rec := range intent.Records {
			changes = append(changes, git.RefChange{Ref: rec.Ref, Old: rec.Old, New: rec.New})
			if rec.Commit != "" {
				commits = append(commits, rec.Commit)
			}
			for _, hash := range []string{rec.Old, rec.New} {
				if git.IsHash(hash) && !git.IsZeroHash(hash) {
					commits = append(commits, hash)
				}
			}
		}
		if err := h.Git.CheckRefChanges(ctx, changes); err != nil {
			if errors.Is(err, git.ErrRefMoved) {
				return false, h.Repos.RejectOperation(ctx, op.ID, err)
			}
			return false, err
		}
		if applyErr := h.Git.ApplyOperation(ctx, op.ID, changes, commits, op.Payload); applyErr != nil {
			// A lost command response does not prove that Git rejected the change.
			applied, err = h.Git.OperationApplied(ctx, op.ID, op.Payload)
			if err != nil {
				return false, err
			}
			if !applied {
				if err := h.Git.CheckRefChanges(ctx, changes); errors.Is(err, git.ErrRefMoved) {
					return false, h.Repos.RejectOperation(ctx, op.ID, err)
				}
				return false, applyErr
			}
		}
	}
	// Person need only identify the already-authorized intent. No fresh permission
	// decision is made on replay; its immutable decision is in each record.
	pc := &pushContext{repo: intent.Repository}
	person, err := h.People.GetByID(ctx, intent.PersonID)
	if err != nil {
		return false, err
	}
	pc.person = person
	h.Ctx.RemoteAddr = intent.RemoteAddr
	return true, h.recordPush(ctx, pc, intent.Records, op.ID)
}
