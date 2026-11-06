# Deploying Xhaos

Xhaos is one static binary (pure Go, the page built in) plus a data folder. Run it

- **on a server or VPS from a release binary**, as a systemd service, when your projects live on that machine wherever you like; or
- **in a container**, when you want the whole thing boxed in and your projects can sit under one folder that you mount.

Either way, put it behind an HTTPS reverse proxy before it is reachable from anywhere but your own machine.

## Requirements

| | |
|---|---|
| System | Linux, amd64 or arm64. (macOS builds run, but agents' commands are not sandboxed there.) |
| Kernel | 5.13+ for the command sandbox (Landlock); 6.7+ (Landlock ABI 4) to use secrets. Check Settings → Tools after starting: it says whether commands are sandboxed and which Landlock version the kernel has. |
| Programs | `bash` and `git`, plus whatever your agents run in your projects (compilers, `npm`, `python3`, `rg`…). Xhaos itself needs nothing else. |
| Memory, disk | Little for Xhaos itself; the data folder grows with transcripts and attachments. Agents' builds use what your projects need. |
| Network | Outbound HTTPS to your model providers. Inbound only through your reverse proxy. |

## On a VPS, from a release binary

Build the archives once (on any machine with Node and Go) with `scripts/release.sh`, or download them from a release. Each `xhaos-<version>-linux-<arch>.tar.gz` holds the `xhaos` binary, the README and this guide; `SHA256SUMS` lists their checksums.

```sh
# On the server:
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf xhaos-*-linux-amd64.tar.gz
sudo install -m 0755 xhaos-*-linux-amd64/xhaos /usr/local/bin/xhaos

# A user of its own: agents' commands run as this user (inside the sandbox).
sudo useradd --system --create-home --home-dir /var/lib/xhaos --shell /bin/bash xhaos
sudo mkdir -p /srv/projects && sudo chown xhaos:xhaos /srv/projects
```

Give it a git identity for the commits agents make, as that user:

```sh
sudo -u xhaos git config --global user.name  "Xhaos agents"
sudo -u xhaos git config --global user.email "agents@example.com"
```

`/etc/systemd/system/xhaos.service`:

```ini
[Unit]
Description=Xhaos agent workspace
After=network-online.target
Wants=network-online.target

[Service]
User=xhaos
Group=xhaos
WorkingDirectory=/var/lib/xhaos
# Listen on localhost only; the reverse proxy is the way in.
Environment=WAILS_SERVER_HOST=127.0.0.1
Environment=WAILS_SERVER_PORT=5180
ExecStart=/usr/local/bin/xhaos -db /var/lib/xhaos/data/xhaos.db -sandbox on \
  -public-host xhaos.example.com -trusted-proxy 127.0.0.1,::1
Restart=on-failure
# Hardening that leaves the sandbox working.
ProtectSystem=strict
ReadWritePaths=/var/lib/xhaos /srv/projects
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now xhaos
journalctl -u xhaos -f      # "locked: agents resume once you sign in with the passphrase"
```

Notes:

- `-sandbox on` refuses to run agents' commands if the kernel cannot sandbox them, instead of quietly running them unsandboxed (`auto`, the default).
- `-public-host` names the domain the proxy serves. A server listening on localhost refuses other `Host` headers (protection against DNS rebinding), so without it every request through the proxy gets 403.
- `-trusted-proxy` lists the proxies whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are believed. With it, wrong passphrases are counted per visitor rather than for the proxy as a whole, and the sign-in cookie is marked `Secure`. Only list your proxy: anyone else could forge those headers.
- Projects can be any folder the `xhaos` user can write. Add them in the Files tab (or `-root DIR` at startup); add each path you use to `ReadWritePaths`.
- Don't add `SystemCallFilter=` to the unit without allowing `landlock_create_ruleset`, `landlock_add_rule`, `landlock_restrict_self` and `seccomp`: the sandbox needs them.

## Reverse proxy (HTTPS)

The page needs HTTPS beyond localhost: browsers only allow some features (copy to clipboard, some storage locks) on secure pages, and the sign-in cookie should never cross the network in the clear.

Caddy (gets and renews a certificate by itself):

```caddyfile
xhaos.example.com {
	reverse_proxy 127.0.0.1:5180
}
```

nginx:

```nginx
server {
    listen 443 ssl http2;
    server_name xhaos.example.com;
    # ssl_certificate … ; ssl_certificate_key … ;

    location / {
        proxy_pass http://127.0.0.1:5180;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        client_max_body_size 25m;          # attachments are up to 20 MB
    }
    # The live event stream: no buffering, long-lived.
    location /xhaos/events {
        proxy_pass http://127.0.0.1:5180;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

Keep the `Host` header as the browser sent it (Caddy does by default): Xhaos checks that a page's `Origin` matches it.

Published channel links (`/xhaos/channel/<workspace>/<id>`) go through the same proxy; they carry their own passphrase instead of a session.

## In a container

The repository's `Dockerfile` builds the page and the server and puts the binary in a small Debian image with `bash`, `git`, `curl`, `python3` and `ripgrep` for agents to use. It runs as an unprivileged user (uid 10001), keeps its data in `/data`, and expects projects under `/projects`.

```sh
docker build -t xhaos .
docker run -d --name xhaos --restart unless-stopped \
  -p 127.0.0.1:5180:5180 \
  -v xhaos-data:/data \
  -v /srv/projects:/projects \
  xhaos -public-host xhaos.example.com -trusted-proxy 172.17.0.1
```

(Arguments after the image name are passed to `xhaos`. With the port published on `127.0.0.1` and a proxy on the host, the requests come from the Docker bridge's gateway, `172.17.0.1` by default; list that, or your proxy container's address, as the trusted proxy.)

Podman runs the same image, rootless:

```sh
podman run -d --name xhaos -p 127.0.0.1:5180:5180 \
  -v xhaos-data:/data -v ~/projects:/projects:Z localhost/xhaos
```

**Projects in a container.** Agents can only see what is mounted into the container, and the server adds projects by their path inside it (`/projects/…`). Either keep the repositories you want agents to work on under one folder and mount that, or mount several: `-v ~/code/api:/projects/api -v ~/work/site:/projects/site`. The files are owned by uid 10001 inside the container; with rootless Podman, that maps into your user's range, so `:U` (Podman) or a `chown` may be needed for agents to write. Git identity: `docker exec -u xhaos xhaos git config --global user.name …` (stored in the container's home: put it in a volume, or bake it into an image of your own).

**The sandbox in a container.** Landlock works in containers and needs no privileges, but the container's seccomp profile must allow its system calls; Docker 25+ and Podman's defaults do. Check Settings → Tools: if it says commands are not sandboxed, upgrade the container engine or pass a seccomp profile that allows `landlock_*`. Never mount the Docker socket into the container, and do not run it `--privileged`: both hand agents the host.

Add the toolchains your projects need with an image of your own:

```dockerfile
FROM xhaos
USER root
RUN apt-get update && apt-get install -y --no-install-recommends golang nodejs npm && rm -rf /var/lib/apt/lists/*
USER xhaos
```

## Security

What Xhaos protects, and what you need to:

- **Agents run commands.** In the sandbox, a command reads and writes only its project (and a shared cache), reads system files and toolchains, and cannot see the rest of the home folder, other projects, Xhaos's data, SSH keys or the SSH agent. Its network is not restricted, except for commands holding secrets. So treat agents like a junior colleague with shell access to their project and the internet: run Xhaos as its own user (or container), never as root, and keep anything sensitive out of the projects you give it.
- **The passphrase** signs browsers in and unlocks the key that encrypts provider keys, MCP secrets and the secrets vault. It is never stored; a lost passphrase cannot be recovered (enter the API keys again in a fresh database). Use a long one. After every restart the server is locked until someone signs in: agents wait until then.
- **Sessions** last 30 days and live in server memory: a restart signs every browser out.
- **HTTPS** only, via the reverse proxy; the server itself should listen on localhost (or the container's network) only.
- **Published channels** let anyone with a channel's passphrase send its agents instructions and read the channel. Publish only channels whose agents you'd let that program instruct; reset or unpublish when done.
- **Secrets** reach only the agents you grant them to, in sealed commands that can reach only the secrets' hosts; each use asks unless you trusted the secret to the agent. A command can still send a value to one of those hosts (a token for `github.com` could be pasted into a gist): review commands that use secrets.
- **Dangerously allow** lifts approvals for one agent for a while; it keeps the sandbox, the secrets rules and a git branch lock. Pushes still need credentials it does not have unless you grant a token; use your remote's branch protection too.
- **MCP servers** you add as local commands run as the server's user, outside the sandbox. Add only ones you trust.
- **One instance per trust boundary.** Workspaces inside one Xhaos share its user, sandbox cache and passphrase. For separate clients or people, run separate instances (one container or system user each, its own data and projects), each behind its own domain on the proxy.

## Backups

Everything lives in the data folder (`/var/lib/xhaos/data`, or the `/data` volume):

| | |
|---|---|
| `xhaos.db` (+ `-wal`, `-shm`) | everything but transcripts and attachments: agents, events index, channels, memories, settings, encrypted keys and secrets |
| `transcripts/` | each agent's transcript as JSON Lines |
| `attachments/` | attached files |
| `sandbox/` | agents' shared tool caches: safe to skip and to delete |

Back up the database consistently: stop the service, or copy it with `sqlite3 xhaos.db ".backup backup.db"` while it runs, then copy `transcripts/` and `attachments/`. The encrypted values are useless without the passphrase, so a backup is safe to keep off the machine, and restoring one needs the passphrase that was current when it was made.

## Upgrading

Replace the binary (or pull and rebuild the image) and restart. The database is migrated forward on start; take a backup first. Sign in again afterwards: the server starts locked.
