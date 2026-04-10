# Gitman

A lightweight, self‑hosted Git hosting service built in Go.  
Zero-CGo, minimal dependencies, easy to deploy, and powered entirely by the system Git + SSH.

---

## Features

- Clean Web UI (HTML + HTMX)
- Create, browse, and manage repositories
- View commits, diffs, branches, file tree, README.md
- SSH-based push/pull using system `git` and `git-shell`
- User accounts with password login
- SSH key management via UI
- Download repositories as ZIP archives
- SQLite-backed metadata storage
- Single static Go binary deployment

---

## Architecture

```
+----------------+
|    Browser     |
| (HTML + HTMX)  |
+-------+--------+
        |
        |  HTTP API (REST + HTML templates)
        |
+-------v--------+
|   Go Backend   |  <--- exec git commands ---------+
| (single binary)|                                  |
+-------+--------+                                  |
        |                                           v
        |                                  +------------------+
+-------v--------------+                   |  Bare Git repos  |
|  SQLite DB           |                   | (filesystem)     |
| (users/keys/repos)   |                   +------------------+
+----------------------+

SSH:
Developer client <-> system SSH server <-> git user (forced command: gitman serve) <-> bare repos
```

---

## Technology Stack

- **Language:** Go
- **Routing:** Chi
- **Templates:** Go `html/template`
- **Dynamic UI:** HTMX
- **Database:** SQLite via `modernc.org/sqlite`
- **Git Integration:** Execute system `git` commands

---

## Backend Modules

### User Management

- Register/login
- Password hashing (bcrypt/scrypt)
- Sessions via secure cookies
- SQLite users table

### SSH Key Management

- Users add/delete public keys
- Keys stored in DB and synced to the configured `authorized_keys` file path (default: `./data/authorized_keys`).
- Keys prefixed to restrict actions:

```
command="/path/to/gitman serve <keyID>",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty ssh-ed25519 AAAA...
```

### Repository Management

- Create repos using:

```
git init --bare ./data/repos/<name>.git
```

- List repos via directory scan or DB index
- Delete repos
- Clone URLs like: `git@server:<user>/<repo>.git`

### Git Browsing APIs

Gitman shells out to Git for all repo data:

| Feature        | Command                                  |
| -------------- | ---------------------------------------- |
| Branches       | `git branch --format="%(refname:short)"` |
| Commit history | `git log --pretty=format:"..."`          |
| Commit detail  | `git show --stat <sha>`                  |
| Tree view      | `git ls-tree <branch>:<path>`            |
| File content   | `git show <branch>:<file>`               |
| README         | `git show <branch>:README.md`            |
| ZIP archive    | `git archive --format=zip <branch>`      |

### Session/Auth Middleware

- Check logged-in users
- Secure cookies (`HttpOnly`)
- Permissions checks

---

## API Overview

| Method | Path                                 | Purpose                      |
| ------ | ------------------------------------ | ---------------------------- |
| GET    | `/`                                  | Dashboard or login           |
| GET    | `/register`                          | Registration form            |
| POST   | `/register`                          | Create new account           |
| GET    | `/login`                             | Login form                   |
| POST   | `/login`                             | Authenticate                 |
| GET    | `/logout`                            | Logout                       |
| GET    | `/keys`                              | List SSH keys                |
| POST   | `/keys`                              | Add SSH key                  |
| POST   | `/keys/{id}/delete`                  | Delete SSH key               |
| GET    | `/repos`                             | List repos                   |
| POST   | `/repos`                             | Create repo                  |
| POST   | `/repos/{repo}/delete`               | Delete repo                  |
| GET    | `/repos/{repo}`                      | Repo home (README + commits) |
| GET    | `/repos/{repo}/branches`             | List branches                |
| GET    | `/repos/{repo}/commits`              | Commit history               |
| GET    | `/repos/{repo}/commits/{sha}`        | Commit diff                  |
| GET    | `/repos/{repo}/tree/{branch}/{path}` | File tree                    |
| GET    | `/repos/{repo}/blob/{branch}/{path}` | File content                 |
| GET    | `/repos/{repo}/archive/{branch}.zip` | Download ZIP                 |

---

## File System Layout

```
./data/repos/
    example.git/
    demo.git/

.data/authorized_keys  # managed by Gitman

/etc/passwd:
  git:x:1001:1001::/home/git:/usr/bin/git-shell
```

---

## Deployment

1. Install system Git and OpenSSH
2. Create `git` user with shell `/usr/bin/git-shell`
3. Build Gitman static Go binary
4. Deploy binary and run service
5. Ensure permissions:
   - `./data/repos` writable by `git`
   - `./data/authorized_keys` writable by web process or sync script

---

## Security

- Use security headers (CSP, HSTS)
- Sanitize all user inputs
- Avoid command injection by escaping Git arguments
- Protect passwords with strong hashes
- Use context/timeouts for Git CLI calls
- SSH locked down to gitman

---

## Development Roadmap

1. HTTP server + routing
2. User auth system
3. SSH key management
4. Repo create/list
5. Repo home view (README + commits)
6. File browser (ls-tree)
7. Commit diff views
8. Branch switching
9. ZIP archive export
10. UI polish + security review

---

## How it All Fits Together

1. User logs in
2. Uploads SSH keys → added to authorized_keys
3. Creates repo → bare Git repo on filesystem
4. Developer uses SSH to push/pull
5. Gitman web UI uses Git CLI to show history, diffs, trees
6. README rendered automatically
7. ZIP archives created via `git archive`
