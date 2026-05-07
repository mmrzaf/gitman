## Gitman Docker Setup Guide

This guide explains how to run Gitman in Docker using HTTP-only or with optional host‑SSH integration. The stack consists of two services: `web` (HTTP server + UI) and `worker` (CI/CD executor), sharing the same data directory.

---

### Prerequisites

- Docker ≥ 20.10
- Docker Compose ≥ 2.0
- (SSH) An existing OpenSSH server on the host (optional)

---

### Quick Start (HTTP Only)

1. Clone the Gitman repository and enter the directory.

2. Create a `.env` file from the template (see below) and set at least `GITMAN_SECRET_KEY`.

3. Start the services:

   ```bash
   docker compose up -d
   ```

4. Access the web UI at `http://localhost:8080`.

All Git operations work via HTTP(S) using username + password or personal access tokens.

---

### Directory Structure

```
.
├── Dockerfile
├── docker-compose.yml
├── .env
├── .env.example
├── data/                  # persisted data (bind‑mounted)
│   ├── db/gitman.sqlite
│   ├── repos/
│   ├── artifacts/
│   └── authorized_keys
└── ... (other source files)
```

---

### Environment Variables

| Variable                    | Default                      | Description                                              |
| --------------------------- | ---------------------------- | -------------------------------------------------------- |
| `GITMAN_PORT`               | `8080`                       | Web server listening port (inside container)             |
| `GITMAN_DB`                 | `/data/db/gitman.sqlite`     | Path to SQLite database file                             |
| `GITMAN_REPOS`              | `/data/repos`                | Directory for bare Git repositories                      |
| `GITMAN_ARTIFACTS`          | `/data/artifacts`            | CI artifacts and logs                                    |
| `GITMAN_AUTH_KEYS`          | `/data/authorized_keys`      | SSH authorized keys file (managed by web)                |
| `GITMAN_SECRET_KEY`         | **required**                 | Passphrase for encrypting CI secrets                     |
| `GITMAN_INTERNAL_URL`       | `http://web:8080`            | Internal web URL used by worker and hooks                |
| `GITMAN_SERVER_HOST`        | `localhost`                  | Public hostname for clone URLs (UX only)                 |
| `GITMAN_LOG_LEVEL`          | `info`                       | Log level (`debug`, `info`, `warn`, `error`)             |
| `GITMAN_WORKER_CONCURRENCY` | `1`                          | Number of concurrent CI jobs                             |
| `GITMAN_BINARY_PATH`        | `/usr/local/bin/gitman-wrap` | Path written into authorized_keys for SSH forced command |

---

### Volumes

All persistent data is stored in the bind‑mounted host directory `./data`.  
This directory is shared between the `web` and `worker` containers.  
**Make sure this directory is owned by the user with UID 1000 (the `gitman` user inside the container) or adjust the Dockerfile accordingly.**

---

### Accessing the Application

- **Web UI**: `http://localhost:8080`
- **Git clone (HTTP)**: `http://localhost:8080/<owner>/<repo>.git`
- **Health check**: `http://localhost:8080/health`

---

### Admin Commands

To run administrative tasks, execute commands inside the `web` container:

```bash
docker compose exec web gitman admin users create <username> <password>
docker compose exec web gitman admin backup-all /tmp/backup
```

---

### Enabling SSH Access (Host Integration)

If you already have an SSH server running on the host, you can let Gitman manage the `authorized_keys` file for the `git` user, while keeping everything else inside Docker.

**Overview:**

- Gitman writes `/data/authorized_keys` with forced commands pointing to `/usr/local/bin/gitman-wrap`.
- The host’s SSH daemon uses that file for the `git` user.
- `gitman-wrap` is a script that calls `docker exec ... gitman serve` inside the running `gitman-web` container.

**Step 1 – Bind mount already in use**  
The `docker-compose.yml` above uses `./data:/data` – this is required so the host can access `authorized_keys`.

**Step 2 – Create the `git` user and wrapper script on the host**

```bash
sudo useradd -m -s /bin/bash git
sudo mkdir -p /home/git/.ssh
sudo chmod 700 /home/git/.ssh
```

Create `/usr/local/bin/gitman-wrap`:

```bash
#!/bin/bash
exec docker exec -i gitman-web gitman "$@"
```

Make it executable:

```bash
sudo chmod +x /usr/local/bin/gitman-wrap
```

**Step 3 – Link the authorized_keys file**

Symlink the shared authorized_keys into the `git` user’s `.ssh` directory:

```bash
sudo ln -s $(pwd)/data/authorized_keys /home/git/.ssh/authorized_keys
sudo chown -h git:git /home/git/.ssh/authorized_keys
sudo chown git:git /home/git/.ssh
```

**Step 4 – Configure host SSH daemon**

Edit `/etc/ssh/sshd_config` and add a match block for the `git` user:

```
Match User git
    AuthorizedKeysFile /home/git/.ssh/authorized_keys
    PasswordAuthentication no
    PermitEmptyPasswords no
```

Restart SSH:

```bash
sudo systemctl restart sshd
```

**Step 5 – Set `GITMAN_BINARY_PATH`**  
In your `.env`, set `GITMAN_BINARY_PATH=/usr/local/bin/gitman-wrap`.  
The web container will now write authorized_keys lines like:

```
command="/usr/local/bin/gitman-wrap serve <keyID>",no-port-forwarding,... ssh-rsa ...
```

**Step 6 – Sync keys**  
When you add an SSH key via the web UI, Gitman regenerates `authorized_keys` and the new key becomes immediately active.  
Test:

```bash
ssh -T git@your-host
```

---

### Backup and Restore

Inside the running `gitman-web` container, run:

```bash
docker compose exec web gitman admin backup-all /tmp/backup
```

The backup directory can be copied out with `docker cp`.  
To restore, bring up a fresh stack, drop the old `data` directory, and copy the backup contents back into `./data`.

---

### Health and Monitoring

- **Web health check**: `GET /health` returns `{"status":"ok"}` when the database is reachable.
- The `docker-compose.yml` includes a health check for the `web` service. Use `docker ps` to monitor status.

Logs can be viewed with:

```bash
docker compose logs -f web
docker compose logs -f worker
```

For production, consider integrating with a log aggregator.

---

### Security Notes

- Replace `GITMAN_SECRET_KEY` with a strong, random passphrase.
- The `data` directory contains sensitive information (database, repositories). Restrict host access accordingly.
- The `GITMAN_INTERNAL_URL` is only reachable inside the Docker network; the web service is not directly exposed to the internet unless you map the port to a public interface (use a reverse proxy in production).
- The SSH setup uses a wrapper script that calls `docker exec`. Only add keys to trusted users.
- For HTTP access, always use a reverse proxy (nginx, Caddy) with TLS termination.

---

### Troubleshooting

| Symptom                                        | Likely cause                           | Solution                                                            |
| ---------------------------------------------- | -------------------------------------- | ------------------------------------------------------------------- |
| Web UI not loading                             | Container not running                  | `docker compose ps` to check status                                 |
| `authorized_keys` not updated after adding key | `GITMAN_BINARY_PATH` not correctly set | Verify the environment variable in the container                    |
| SSH clone hangs                                | `docker exec` cannot reach container   | Ensure `gitman-web` is running and the wrapper script is executable |
| Permission errors on data directory            | UID mismatch                           | `sudo chown -R 1000:1000 ./data`                                    |

---

This setup gives you a self‑contained, production‑ready Gitman deployment with optional SSH support, fully manageable through Docker Compose.
