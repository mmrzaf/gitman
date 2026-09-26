package repo

import (
	"context"
	"crypto/aes"
	cryptocipher "crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/mmrzaf/gitman/internal/git"
	"github.com/mmrzaf/gitman/internal/postgres"
)

const repoColumns = `id, name, description, default_branch, visibility, default_push_policy, default_push_people, created_by, created_at`

func scanRepo(row interface{ Scan(...any) error }) (*Repo, error) {
	r := &Repo{}
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.DefaultBranch, &r.Visibility,
		&r.DefaultPushPolicy, &r.DefaultPushPeople, &r.CreatedBy, &r.CreatedAt); err != nil {
		return nil, err
	}
	return r, nil
}

func insertRepo(ctx context.Context, tx postgres.Tx, id, name, description, defaultBranch, personID string) (*Repo, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO repos (id, name, description, default_branch, created_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING `+repoColumns,
		id, name, description, defaultBranch, personID)
	repo, err := scanRepo(row)
	if err != nil {
		return nil, postgres.NormalizeWrite(err)
	}
	return repo, nil
}

func selectRepoByName(ctx context.Context, q postgres.Querier, name string) (*Repo, error) {
	repo, err := scanRepo(q.QueryRow(ctx, `SELECT `+repoColumns+` FROM repos WHERE name = $1`, name))
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return repo, nil
}

func selectRepoByID(ctx context.Context, q postgres.Querier, repoID string) (*Repo, error) {
	repo, err := scanRepo(q.QueryRow(ctx, `SELECT `+repoColumns+` FROM repos WHERE id = $1`, repoID))
	if err != nil {
		return nil, postgres.NormalizeNotFound(err)
	}
	return repo, nil
}

func selectRepos(ctx context.Context, q postgres.Querier) ([]*Repo, error) {
	rows, err := q.Query(ctx, `SELECT `+repoColumns+` FROM repos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()
	var result []*Repo
	for rows.Next() {
		repo, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repository: %w", err)
		}
		result = append(result, repo)
	}
	return result, rows.Err()
}

func selectReadableRepos(ctx context.Context, q postgres.Querier, personID string) ([]*Repo, error) {
	rows, err := q.Query(ctx, `
		SELECT `+repoColumns+` FROM repos
		WHERE visibility = 'everyone'
		   OR EXISTS (SELECT 1 FROM repo_readers WHERE repo_readers.repo_id = repos.id AND repo_readers.person_id = $1)
		ORDER BY name
	`, personID)
	if err != nil {
		return nil, fmt.Errorf("list readable repositories: %w", err)
	}
	defer rows.Close()
	var result []*Repo
	for rows.Next() {
		repo, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repository: %w", err)
		}
		result = append(result, repo)
	}
	return result, rows.Err()
}

func selectIsReader(ctx context.Context, q postgres.Querier, repoID, personID string) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM repo_readers WHERE repo_id = $1 AND person_id = $2)
	`, repoID, personID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check reader: %w", err)
	}
	return exists, nil
}

func updateVisibilityRow(ctx context.Context, q postgres.Querier, repoID string, v Visibility) error {
	tag, err := q.Exec(ctx, `UPDATE repos SET visibility = $2 WHERE id = $1`, repoID, v)
	if err != nil {
		return fmt.Errorf("update visibility: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func insertReaderRow(ctx context.Context, q postgres.Querier, repoID, personID string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO repo_readers (repo_id, person_id) VALUES ($1, $2)
		ON CONFLICT (repo_id, person_id) DO NOTHING
	`, repoID, personID); err != nil {
		return fmt.Errorf("add reader: %w", err)
	}
	return nil
}

func deleteReaderRow(ctx context.Context, q postgres.Querier, repoID, personID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM repo_readers WHERE repo_id = $1 AND person_id = $2`, repoID, personID)
	if err != nil {
		return fmt.Errorf("remove reader: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func selectReaderIDs(ctx context.Context, q postgres.Querier, repoID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT person_id FROM repo_readers WHERE repo_id = $1 ORDER BY added_at`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list readers: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var personID string
		if err := rows.Scan(&personID); err != nil {
			return nil, fmt.Errorf("scan reader: %w", err)
		}
		ids = append(ids, personID)
	}
	return ids, rows.Err()
}

func updateDefaultPushRow(ctx context.Context, q postgres.Querier, repoID string, policy PushPolicy, people []string) error {
	if people == nil {
		people = []string{}
	}
	tag, err := q.Exec(ctx, `
		UPDATE repos SET default_push_policy = $2, default_push_people = $3 WHERE id = $1
	`, repoID, policy, people)
	if err != nil {
		return fmt.Errorf("update default push: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func deleteRepoRow(ctx context.Context, tx postgres.Tx, repoID string) (name string, err error) {
	if err := tx.QueryRow(ctx, `DELETE FROM repos WHERE id = $1 RETURNING name`, repoID).Scan(&name); err != nil {
		return "", postgres.NormalizeNotFound(err)
	}
	return name, nil
}

func updateDescriptionRow(ctx context.Context, q postgres.Querier, repoID, description string) error {
	tag, err := q.Exec(ctx, `UPDATE repos SET description = $2 WHERE id = $1`, repoID, description)
	if err != nil {
		return fmt.Errorf("update description: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func selectRules(ctx context.Context, q postgres.Querier, repoID string) ([]Rule, error) {
	rows, err := q.Query(ctx, `
		SELECT kind, pattern, push_policy, push_people, allow_force, allow_delete,
		       run_on_push, allow_docker, allow_secrets, allow_ship
		FROM ref_rules WHERE repo_id = $1 ORDER BY kind, pattern
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	defer rows.Close()
	var rules []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Kind, &r.Pattern, &r.PushPolicy, &r.PushPeople, &r.AllowForce, &r.AllowDelete,
			&r.RunOnPush, &r.AllowDocker, &r.AllowSecrets, &r.AllowShip); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func upsertRule(ctx context.Context, tx postgres.Tx, id, repoID string, r Rule, personID string) error {
	people := r.PushPeople
	if people == nil {
		people = []string{}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ref_rules (id, repo_id, kind, pattern, push_policy, push_people, allow_force, allow_delete,
		                       run_on_push, allow_docker, allow_secrets, allow_ship, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''))
		ON CONFLICT (repo_id, kind, pattern) DO UPDATE SET
			push_policy = EXCLUDED.push_policy, push_people = EXCLUDED.push_people,
			allow_force = EXCLUDED.allow_force, allow_delete = EXCLUDED.allow_delete,
			run_on_push = EXCLUDED.run_on_push, allow_docker = EXCLUDED.allow_docker,
			allow_secrets = EXCLUDED.allow_secrets, allow_ship = EXCLUDED.allow_ship
	`, id, repoID, r.Kind, r.Pattern, r.PushPolicy, people, r.AllowForce, r.AllowDelete,
		r.RunOnPush, r.AllowDocker, r.AllowSecrets, r.AllowShip, personID)
	if err != nil {
		return fmt.Errorf("save rule: %w", err)
	}
	return nil
}

func deleteRuleRow(ctx context.Context, tx postgres.Tx, repoID string, kind git.Kind, pattern string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM ref_rules WHERE repo_id = $1 AND kind = $2 AND pattern = $3`, repoID, kind, pattern)
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

func selectRefs(ctx context.Context, q postgres.Querier, repoID string) ([]IndexedRef, error) {
	rows, err := q.Query(ctx, `
		SELECT kind, name, commit_hash, updated_at, updated_by
		FROM refs WHERE repo_id = $1 ORDER BY updated_at DESC, kind, name
	`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	defer rows.Close()
	var refs []IndexedRef
	for rows.Next() {
		var r IndexedRef
		if err := rows.Scan(&r.Kind, &r.Name, &r.Commit, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, fmt.Errorf("scan ref: %w", err)
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// lockRefIndex takes a lock on one repository's ref index, held until
// tx ends.
func lockRefIndex(ctx context.Context, tx postgres.Tx, repoID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gitman.refs.' || $1, 0))`, repoID); err != nil {
		return fmt.Errorf("lock ref index: %w", err)
	}
	return nil
}

// syncRefsRows makes the refs table match kinds/names/commits exactly:
// it deletes rows for refs no longer present and upserts the rest,
// touching updated_at/updated_by only for a ref whose commit changed.
func syncRefsRows(ctx context.Context, q postgres.Querier, repoID string, kinds, names, commits []string, personID string) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM refs
		WHERE repo_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM unnest($2::text[], $3::text[]) AS u(k, n)
			WHERE u.k = refs.kind AND u.n = refs.name
		  )
	`, repoID, kinds, names); err != nil {
		return fmt.Errorf("remove stale refs: %w", err)
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO refs (repo_id, kind, name, commit_hash, updated_at, updated_by)
		SELECT $1, u.k, u.n, u.c, now(), NULLIF($5, '')
		FROM unnest($2::text[], $3::text[], $4::text[]) AS u(k, n, c)
		ON CONFLICT (repo_id, kind, name) DO UPDATE SET
			commit_hash = EXCLUDED.commit_hash,
			updated_at  = now(),
			updated_by  = EXCLUDED.updated_by
		WHERE refs.commit_hash <> EXCLUDED.commit_hash
	`, repoID, kinds, names, commits, personID); err != nil {
		return fmt.Errorf("update refs: %w", err)
	}
	return nil
}

func selectSecrets(ctx context.Context, q postgres.Querier, repoID string) ([]Secret, error) {
	rows, err := q.Query(ctx, `SELECT key, created_at, created_by FROM secrets WHERE repo_id = $1 ORDER BY key`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()
	var result []Secret
	for rows.Next() {
		var s Secret
		if err := rows.Scan(&s.Key, &s.CreatedAt, &s.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func upsertSecretRow(ctx context.Context, tx postgres.Tx, id, repoID, key string, ciphertext, nonce []byte, personID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO secrets (id, repo_id, key, ciphertext, nonce, created_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		ON CONFLICT (repo_id, key) DO UPDATE SET
			ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce,
			created_at = now(), created_by = EXCLUDED.created_by
	`, id, repoID, key, ciphertext, nonce, personID)
	if err != nil {
		return fmt.Errorf("save secret: %w", err)
	}
	return nil
}

func deleteSecretRow(ctx context.Context, tx postgres.Tx, repoID, key string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM secrets WHERE repo_id = $1 AND key = $2`, repoID, key)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return postgres.ErrNotFound
	}
	return nil
}

// secretContext is what a sealed secret is bound to: its repository and
// its name. A ciphertext copied to another repository's row, or to
// another name, fails to decrypt instead of becoming that secret.
func secretContext(repoID, name string) []byte {
	return []byte(repoID + "\x00" + name)
}

// encryptSecret seals plaintext under key, the instance's configured
// GITMAN_SECRET_KEY, bound to boundTo (see secretContext), and returns
// the ciphertext and the nonce used, which the caller stores alongside
// it — the same shape as the secrets table's ciphertext and nonce
// columns. Every call uses a fresh random nonce, so encrypting the same
// value twice never produces the same bytes.
func encryptSecret(key, plaintext string, boundTo []byte) (ciphertext, nonce []byte, err error) {
	gcm, err := secretCipher(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return gcm.Seal(nil, nonce, []byte(plaintext), boundTo), nonce, nil
}

// decryptSecret reverses encryptSecret. It fails if key or boundTo does
// not match the one the value was sealed with, or if the ciphertext has
// been altered.
func decryptSecret(key string, ciphertext, nonce, boundTo []byte) (string, error) {
	gcm, err := secretCipher(key)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, boundTo)
	if err != nil {
		return "", fmt.Errorf("decrypt: wrong key, or the value was altered or moved")
	}
	return string(plaintext), nil
}

// secretCipher builds an AES-256-GCM instance from key. key is the
// operator's GITMAN_SECRET_KEY, expected to already be high-entropy
// (config requires at least 32 characters), so a fast hash is enough to
// turn it into a fixed-size AES key; there is no low-entropy human
// passphrase here to defend against with a slow KDF.
func secretCipher(key string) (cryptocipher.AEAD, error) {
	if key == "" {
		return nil, fmt.Errorf("GITMAN_SECRET_KEY is not configured")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("initialize cipher: %w", err)
	}
	gcm, err := cryptocipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize GCM: %w", err)
	}
	return gcm, nil
}

func selectSecretValues(ctx context.Context, q postgres.Querier, repoID string) (map[string]struct{ ciphertext, nonce []byte }, error) {
	rows, err := q.Query(ctx, `SELECT key, ciphertext, nonce FROM secrets WHERE repo_id = $1`, repoID)
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	defer rows.Close()
	result := map[string]struct{ ciphertext, nonce []byte }{}
	for rows.Next() {
		var key string
		var v struct{ ciphertext, nonce []byte }
		if err := rows.Scan(&key, &v.ciphertext, &v.nonce); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		result[key] = v
	}
	return result, rows.Err()
}
