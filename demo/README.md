# RepairFlow demo

Synthetic, disposable RepairFlow data for trying the UI, screenshots, contributor
development and manual testing. The dataset is generated from
[`internal/seed`](../internal/seed); **no SQLite database is committed**. A
prebuilt database would drift with schema migrations, create binary diffs and
risk carrying local data into the public tree.

## One-shot demo

From a fresh clone:

```bash
cd demo
./run-demo.sh
```

The launcher uses, in order:

1. `REPAIRFLOW_BIN`;
2. a built `repairflow` in the repository root;
3. a cached `demo/bin/repairflow`;
4. `repairflow` on `PATH`;
5. a source build when Go 1.27 and `make` are available;
6. otherwise the matching **Linux** release archive from GitHub, extracted into
   `demo/bin/` with `SHA256SUMS` verification.

Then it creates a temporary workspace, seeds the synthetic demo, starts the
server on `127.0.0.1:8091` and removes the workspace when you press Ctrl+C.

Downloads are Linux-only (`amd64`/`arm64`) and require `curl` or `wget`, `tar`
and `sha256sum`. To use a prerelease candidate explicitly:

```bash
REPAIRFLOW_DEMO_TAG=v0.2.0-rc1 ./run-demo.sh
```

For offline use, set `REPAIRFLOW_DEMO_NO_DOWNLOAD=1`; the launcher then fails
with instructions instead of fetching. `REPAIRFLOW_DEMO_BASE_URL` can point at a
release mirror or a local test server.


Open <http://127.0.0.1:8091/login> and sign in with:

```text
Username: demo
Password: repairflow
```

The launcher is loopback-only. The credentials are public and safe only for
this synthetic workspace; never use them for a real workspace.

## Persistent development workspace

For development, keep one seeded workspace instead of recreating it:

```bash
cd demo
./dev.sh
```

The first run creates `demo/workspace/` (gitignored) with the full demo dataset
and the demo account. Later runs reuse it. Stop with Ctrl+C; the workspace stays.
Reset it while its content is still pristine:

```bash
./reset.sh
```

Reset refuses a workspace that was edited or contains anything other than the
unchanged demo dataset. Create a new directory or remove `demo/workspace/` if
you want a fresh one.

## What is in the demo

The seed covers every repair stage, archived and picked-up records, unassigned
repairs, entry revisions and soft deletes, whole-piece parts with unknown cost,
removed part lines, captions, entry links and generated attachment bytes. Names, devices, phone numbers, email addresses and media are
fictional and synthetic. All records are created through the same
application services as real data, not by SQL shortcuts.

The dataset version is `demo-v5`. Source, counts and generated media live in
[`internal/seed/seed.go`](../internal/seed/seed.go) and
[`internal/seed/media.go`](../internal/seed/media.go). Change the dataset there
and add a regression test; do not commit a database or exported media tree.

## Commands

| Command | Purpose |
|---|---|
| `./run-demo.sh` | Temporary workspace, foreground server, cleanup on exit |
| `./dev.sh` | Persistent `demo/workspace/` for development |
| `./reset.sh` | Restore a pristine synthetic workspace |

Equivalent repository targets: `make demo` and `make demo-dev`.

Do not put real customer data, private photos, passwords or local hostnames in
this directory. `demo/workspace/` is ignored by git.
