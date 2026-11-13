---
name: repairflow-deploy
description: Release, install, back up and upgrade RepairFlow.
---

# RepairFlow deploy and operate

Use this when packaging a release or moving RepairFlow to a Linux host. Read the authoritative
procedures in `docs/DEPLOY.md`, `docs/INSTALL.md` and `docs/USAGE.md` before executing.

## Release artifact

```bash
scripts/release.sh --version X.Y.Z [--key /secure/release-signing.key] --out /fresh/output
```

The script refuses a dirty tree, a `-dev` version and an existing output directory. It builds
`linux/amd64` and `linux/arm64`, writes a manifest naming the binary as `repairflow`, signs it, embeds
the public key, verifies the manifest with a throwaway checker build, then writes exactly:

```text
repairflow_Linux_x86_64.tar.gz
repairflow_Linux_arm64.tar.gz
SHA256SUMS
```

Each archive contains only `repairflow`, `LICENSE`, `manifest.json`, `manifest.sig` and
`update-github.sh`; `SHA256SUMS` covers the two archives. Do not upload loose
binaries, README, source archives, demo scripts or manifests as release assets. The GitHub-generated
source archives at the tag satisfy source availability. `make license-check` must pass before
packaging.

Never put a private signing key, build output or release directory into git. Keep signing keys
offline and out of the repository, chat and logs.

## Copy and verify

```bash
scp repairflow_Linux_x86_64.tar.gz repairflow_Linux_arm64.tar.gz SHA256SUMS technician@host:~/
# on the target (both archives present: BusyBox sha256sum has no --ignore-missing)
sha256sum -c SHA256SUMS
tar -xzf repairflow_Linux_x86_64.tar.gz
install -Dm0755 repairflow ~/.local/bin/repairflow
install -Dm0755 update-github.sh ~/.local/bin/update-github.sh
~/.local/bin/repairflow version
```

The binary must live at a stable absolute path before installing a service: the systemd unit pins
that path and offline updates stage `<exe>.new` beside it.

## First run

```bash
# disposable synthetic check, from a source checkout
./scripts/run-demo.sh

# real workspace
repairflow init --home /srv/repairflow
REPAIRFLOW_NEW_PASSWORD='a-long-password' repairflow user add alice --role admin --home /srv/repairflow
repairflow doctor --home /srv/repairflow
repairflow serve --home /srv/repairflow
```

The UI is loopback-only by default. Reach it through an SSH tunnel or a TLS-terminating overlay;
never expose a plain LAN listener holding real records. `web.allow_insecure_lan=true` is a
testing-only exception.

## Service

```bash
repairflow service install --home /srv/repairflow     # systemd user unit
repairflow service status
# Alpine/OpenRC, with doas or sudo:
doas repairflow service install --init openrc --home /srv/repairflow
rc-service repairflow status
```

`--name NAME` creates a second independent instance with its own workspace, port, binary path and
unit. A unit whose marker belongs to another instance is refused, not overwritten. Removing a
service must not delete a hand-written unit.

## Backup, restore and upgrade

```bash
repairflow backup create --output /ABS/pre-upgrade.rfbak --home /srv/repairflow
repairflow backup verify /ABS/pre-upgrade.rfbak
repairflow restore /ABS/pre-upgrade.rfbak --home /srv/repairflow-new
repairflow doctor --home /srv/repairflow-new
```

A restore targets an empty/new workspace and verifies structure, hashes, schema and attachment
references before publishing. Keep archives on separate storage; they contain customer data and
password hashes and are not encrypted by the application.

Signed GitHub update (exact tag, check first, then apply):

```bash
./update-github.sh --tag X.Y.Z --binary "$HOME/.local/bin/repairflow"
./update-github.sh --tag X.Y.Z --binary "$HOME/.local/bin/repairflow" --apply
```

The POSIX script downloads the exact architecture archive, rejects unsafe paths, verifies the signed
manifest and artifact hash/size, and leaves service restart to the operator. Install the archive's
`update-github.sh` beside the binary. For an offline update, unpack the same archive and use the
`manifest.json`, `manifest.sig` and `repairflow` it contains.

Signed offline update:

```bash
repairflow update check --manifest manifest.json --signature manifest.sig
repairflow update apply --file repairflow --manifest manifest.json \
  --signature manifest.sig --confirm
repairflow service install          # re-pin the unit if the path changed
repairflow doctor
```

Unsigned builds cannot use `update apply`; copy the binary and re-run `service install` instead.
Never disable signature, hash, size or path checks to make an upgrade pass. Verified apply is
Linux-only; other platforms must refuse.

## Safety and limits

- Never deploy against another operator's workspace, installed service or customer data.
- Back up before a destructive operation or upgrade; test the archive first.
- One workspace per instance; no sync, shared server mode, telemetry or outbound calls.
- Plain HTTP inside the app, no TLS termination, no multi-user isolation and no encryption at rest.
  Tailscale/SSH or another TLS proxy is a host-level concern.
- The workspace-owning OS account is the trust boundary; web roles are cooperative, not a sandbox.
- Do not push, tag, publish or deploy without explicit owner authorization.
- RepairFlow is AGPL-3.0-only; preserve `LICENSE` in every distributed bundle. Do not relicense the
  project without the owner.
