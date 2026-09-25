# Production rollout runbook

Operator-only: review each step before running it against production. Do not execute this runbook from an automated worker.

Approved-checkout note: this block assumes the approved artifact checkout at `~/projects/cpa-plugin-auto-router`; the current worktree may differ.

```bash
# 1. Binary and table (no restart; the host loads the .so on the next config apply)
install -m 0644 ~/projects/cpa-plugin-auto-router/bin/auto-router.so ~/cliproxyapi/plugins/auto-router.so
mkdir -p ~/cliproxyapi/plugins/auto-router
install -m 0644 ~/cliproxyapi-test/plugins/auto-router/models.yaml ~/cliproxyapi/plugins/auto-router/models.yaml

# 2. Secret for the plugin: single-key env file, 0600, file-to-file copy
grep -E '^OPENROUTER_API_KEY=' ~/.hermes/.env > ~/cliproxyapi/plugins/auto-router/env && chmod 600 ~/cliproxyapi/plugins/auto-router/env
# add to ~/.config/systemd/user/cliproxyapi.service [Service]:  EnvironmentFile=-/home/hermes/cliproxyapi/plugins/auto-router/env
# systemctl --user daemon-reload   ← does NOT restart; the env line takes effect on the NEXT restart,
# so until then the plugin runs with jev-unavailable (routine/high) — still functional. Schedule the
# restart for a quiet moment.

# 3. Enable in config.yaml (hot-reloaded): set plugins.enabled: true, replace the `example` entry with
#     auto-router: {enabled: true, priority: 10, jev_api_key_env: OPENROUTER_API_KEY,
#                   table_path: /home/hermes/cliproxyapi/plugins/auto-router/models.yaml}
# 4. Verify without restart
journalctl --user -u cliproxyapi -n 40 --no-pager | grep -i "auto-router"
curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:8317/v1/models | grep -o '"auto-router"'

# 5. Point the weekly timer at production: edit ~/.config/systemd/user/cpa-auto-router-update.service
#    --catalog http://127.0.0.1:8317 --catalog-key-file <0600 file with the 8317 key>
#    --out /home/hermes/cliproxyapi/plugins/auto-router/models.yaml ; daemon-reload.

# Rollback: set auto-router.enabled: false in config.yaml (hot-reload) — no restart.
```
