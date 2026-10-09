# Production rollout runbook

Operator-only: review each step before running it against production. Do not execute this runbook from an automated worker.

## Where production runs

Since 2026-09-29 the production proxy runs in **LXC 139 `cliproxy`** (Proxmox node `pve1`), not on hermes-chloe.

| Item | Value |
|---|---|
| Host | `root@10.23.23.12` (SSH key of hermes-chloe) |
| Service | system unit `cliproxyapi.service`, runs as user `cliproxy` |
| Proxy root | `/opt/cliproxy` (`config.yaml`, `auth/`, `plugins/`) |
| Plugin binary | `/opt/cliproxy/plugins/auto-router*.so` (exactly one file) |
| Plugin table and secrets | `/opt/cliproxy/plugins/auto-router/` (`models.yaml`, `env`, `api-key`) |
| Endpoint | `http://10.23.23.12:8317/v1` |

`localhost:8317` on hermes-chloe is now only a TCP forwarder (`cliproxy-forward.socket` user unit) to the LXC. `~/cliproxyapi/` on hermes-chloe is the **retired** proxy: its `cliproxyapi.service` user unit is disabled and must stay off, because the OAuth refresh tokens rotate and two proxies with the same credentials invalidate each other. Anything written to `~/cliproxyapi/plugins/` never reaches production.

The TEST instance (`cliproxyapi-test.service`, port 8318, `~/cliproxyapi-test/`) stays on hermes-chloe and is unchanged.

## Deploy

Approved-checkout note: this block assumes the approved artifact checkout at `~/projects/cpa-plugin-auto-router`; the current worktree may differ.

```bash
# 1. Binary: build, then replace the single plugin file on the LXC. `make install` removes any older
#    auto-router*.so first, so the host never loads two copies. No restart: the host reloads it.
make install

# 2. Table (only when it changed)
scp table/models.yaml root@10.23.23.12:/opt/cliproxy/plugins/auto-router/models.yaml
ssh root@10.23.23.12 'chown cliproxy:cliproxy /opt/cliproxy/plugins/auto-router/models.yaml'

# 3. Plugin secret (already in place; only when rotating): single-key env file, 0600, copied file-to-file.
#    The unit reads it via EnvironmentFile=/opt/cliproxy/plugins/auto-router/env, so a rotation needs
#    `ssh root@10.23.23.12 systemctl restart cliproxyapi`.

# 4. Verify
ssh root@10.23.23.12 'journalctl -u cliproxyapi -n 40 --no-pager | grep -i "auto-router"'   # expect "plugin loaded"
# then send one request with model "auto-router" to http://10.23.23.12:8317/v1/chat/completions

# 5. Weekly table updater: ~/.config/systemd/user/cpa-auto-router-update.service already targets
#    production. It runs the updater against http://127.0.0.1:8317 (the TCP forwarder to the LXC) and
#    writes ~/cliproxyapi/plugins/auto-router/models.yaml, then its ExecStartPost scp's that file to
#    the LXC as .models.yaml.new, installs it as cliproxy:cliproxy 0600, and atomically renames it
#    into place over the live models.yaml. No restart: the plugin's mtime watcher hot-reloads the new
#    table and logs "auto-router table reloaded". A model newly visible in the catalog is still
#    silently dropped (WARN "no tier") until it has an entry in both table/tiers.yaml and
#    updater/aliases.py; add both before the next scheduled run picks it up.

# Rollback: set plugins.configs.auto-router.enabled: false in /opt/cliproxy/config.yaml on the LXC
# (hot-reload, no restart).
```
