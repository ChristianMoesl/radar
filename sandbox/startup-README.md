# Your sandbox startup scripts

Add regular executable files here, for example `10-git-config` and `20-tools`.
Run `chmod +x <script>` on the host. This directory is mounted read-only;
scripts execute **inside the sandbox**, as the non-root `agent` user (UID 1000).
This README is not executable and is ignored. With no scripts, startup succeeds
without doing any extra work.

Use a **custom image** for reusable toolchains, system packages or expensive
installs. Use **startup scripts** for runtime configuration or lightweight
initialization without rebuilding an image. Both approaches can be combined.
See https://github.com/ChristianMoesl/radar/blob/main/docs/integrations/sbx.md
for custom images, credentials and troubleshooting.

Example `10-git-config` (replace the example identity):

```sh
#!/bin/sh
set -eu
git config --global user.name "Example Developer"
git config --global user.email "developer@example.com"
```

Scripts run in byte-sorted filename order, with this directory as their working
directory and no interactive stdin. Use a shebang, fail on errors, and keep them
idempotent: they run after each sandbox restart, and a failed attempt can retry
from the beginning. Each script is a separate process, not sourced; exporting a
variable does not affect later scripts or tool calls. Write persistent tool
configuration instead. Do not store private keys or tokens in scripts.

The image supplies `sandbox-startup`. The kit starts `sandbox-startup run` and
Radar's `ready_command` also runs it: a lock serializes concurrent callers, and
successful initialization is skipped for the rest of that boot. Early Pi tools
independently wait for readiness through pi-sbx. Radar bounds its readiness
command to 60 seconds; put lengthy installs in an image instead.

Editing a file does not rerun successful hooks. Restart the sandbox to apply
changes. After fixing a failed hook, `sbx exec <name> sandbox-startup run` retries
it. Failures leave the sandbox accessible, but dependent work remains blocked.
Private diagnostics live inside the sandbox at
`~/.cache/sandbox-startup/{status.json,output.log}`. Logs may contain secrets;
inspect locally and do not paste them unredacted.

Scripts and this README are user-owned. Radar setup and upgrades never overwrite
them. Sandbox-local manual changes can disappear on recreation: keep durable
customizations in these scripts, your kit or your image.
