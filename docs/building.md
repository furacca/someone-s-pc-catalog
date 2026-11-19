# Building and publishing catalogs

For existing archives, see the [README](../README.md). The [workflow diagram](workflow.md) shows how the steps below fit together.

## Requirements

Podman, Git, GNU tar, gzip, jq, curl, Go 1.26.11 or later, network access and about 2 GB of disk.

Start from a checkout of this repository:

```sh
git clone https://github.com/furacca/someone-s-pc-catalog.git
cd someone-s-pc-catalog
```

## Select the source

[tcgdex/pin.env](../tcgdex/pin.env) records the source and builder inputs. `TCGDEX_COMMIT` must always be a full 40-character commit available in `TCGDEX_REPO`.

| Mode | `TCGDEX_TAG` | `TCGDEX_BASE_TAG` | Generated release tag |
| --- | --- | --- | --- |
| Upstream release | Its tag, such as `v2.49.0` | Empty | `someone-s-pc-catalog-2.49.0` |
| Snapshot | Empty | Ancestor release, such as `v2.49.0` | `someone-s-pc-catalog-2.49.0-4218f64dcbf3` |

Leave `RELEASE_TAG` empty to generate the name above. Version tags use `v<major>.<minor>.<patch>`; snapshots append the first 12 characters of `TCGDEX_COMMIT`.

For a tagged release, choose a [TCGdex release](https://github.com/tcgdex/cards-database/releases) and record its full commit SHA. Preparation checks that the tag points to that commit.

If you fix how this repository builds a published catalog, keep the source tag, base tag and commit unchanged, bump `BUILDER_VERSION` and set `RELEASE_TAG` to the generated name followed by `-v2`, then `-v3` for another correction. For example, use `someone-s-pc-catalog-2.49.0-v2` for the release or `someone-s-pc-catalog-2.49.0-4218f64dcbf3-v2` for the snapshot.

For a snapshot containing a fix that has not been tagged upstream, set:

```sh
TCGDEX_TAG=""
TCGDEX_BASE_TAG="v2.49.0"
TCGDEX_COMMIT="<full 40-character commit SHA>"
RELEASE_TAG=""
```

The commit stays fixed until you change the pin. Preparation checks that `TCGDEX_BASE_TAG` exists and is an ancestor of the snapshot commit. If it points exactly at that commit, use the upstream release mode instead. Keep `TCGDEX_TAG` empty for snapshots: the base tag does not describe their exact source.

`prepare` assigns the UTC build revision independently of the release name; never enter the revision by hand. Both release modes produce production catalogs accepted by SOMEONE'S PC.

## Build a candidate

To build the checked-in inputs:

```sh
make test
make prepare
```

When selecting a different upstream tag or commit, run `make capture-assets` once before preparing. It replaces `tcgdex/assets-index.json.gz` with a fresh CDN index. Keep that frozen index while reviewing or retrying the build.

| Command | Purpose |
| --- | --- |
| `make test` | Run Go vet and tests |
| `make capture-assets` | Capture the image index for new source inputs |
| `make prepare` | Build and validate a candidate archive, lock and report |
| `make diff` | Compare the candidate with the current locked archive |

`prepare` performs these steps:

1. Assigns a UTC revision newer than the root lock, builds `catalogtool` and unpacks the frozen image index
2. Clones the pinned source commit, verifies the exact release tag or snapshot ancestry and applies `tcgdex/util.ts.patch`
3. Builds the Bun container and installs locked dependencies, then runs the compiler contract tests and compilation without network access
4. Normalizes the generated JSON and preserves the unpatched source checkout plus compiled files in `source/upstream.tar.gz`
5. Projects the application catalog, writes the ZIP, lock and report, then inspects the archive

The Bun base image is pinned by digest. [builder.Dockerfile](../tcgdex/builder.Dockerfile) adds Git for the compiler's edit dates. The projection includes English physical-card sets and available application translations; Pocket and other source languages remain in the preserved snapshot.

Outputs are written under `.cache/tcgdex/`; `WORK_DIR` changes that directory:

| File | Purpose |
| --- | --- |
| `<release-tag>.zip` | Candidate archive, named after its release tag |
| `candidate.lock.json` | Latest candidate's identity, checksum, size and release URL |
| `candidate.report.txt` | Latest candidate's counts, translation coverage and source findings |

The root `catalog.lock.json` still identifies the previous archive until you copy `candidate.lock.json` over it. `make diff` uses the candidate lock to find the archive from the last successful build, including snapshots. `prepare` prints the archive path, release tag and title to use. Each build gets a UTC revision, so compare catalog content with `make diff` rather than expecting identical archive checksums.

The builder stages each output in a sibling directory named `.<filename>.catalog-stage`. It keeps a hard link to each previous file until all three replacements succeed. A write or rename error rolls back the build. If a process is killed during replacement, confirm that no build is running, move any remaining staging directories aside and rerun the build with the same output paths before using the candidate. The `previous` files in those directories are the backups.

### Frozen image index

The TCGdex compiler reads the image CDN's `datas.json` inventory to decide which image, logo and symbol URLs to include. The inventory lists available asset paths; it contains no scans or card definitions.

The live inventory changes: if TCGdex adds a missing image tomorrow, the same card sources produce a catalog with a new image URL and a different checksum. `tcgdex/assets-index.json.gz` is the copy captured for a release, so every checkout builds from the same input. Freezing it does not freeze the image files the CDN serves.

### Network access

- `make prepare` makes no requests to the TCGdex API or image CDN: it uses the source checkout and frozen image index
- `make capture-assets` downloads the CDN index once per run, plus any redirects; it does not download card images
- Git checkout, container setup and dependency installation require network access; compilation runs with `--network none`

## Review the candidate

Run `make diff` while the root lock still describes the previous archive. Check the changes and candidate report: missing sets or cards, printing changes, incomplete sets and translation coverage.

Comparison downloads the previous archive from the lock's `artifact_url`. To use a local copy instead:

```sh
PREVIOUS_CATALOG=/path/to/previous/someone-s-pc-catalog-2.49.0.zip make diff
```

To inspect a downloaded release directly, for example:

```sh
go run ./cmd/catalogtool inspect -archive someone-s-pc-catalog-2.49.0.zip -lock catalog.lock.json
```

## Publish

1. Copy `.cache/tcgdex/candidate.lock.json` to the root `catalog.lock.json`
2. Commit and push the lock and all source or build changes, including changed files in `tcgdex/`; keep archives and `.cache/` out of Git
3. On GitHub, draft a release on the pushed commit using the tag and title printed by `prepare`: `SOMEONE'S PC catalog for TCGdex <version> — revision <revision>` for a tagged source, or `SOMEONE'S PC catalog for TCGdex <base version> + <short commit SHA> — revision <revision>`
4. Attach the ZIP printed by `prepare` and the root `catalog.lock.json`, add the notes below and publish
5. Download and [verify the published files](../README.md#verify-the-download), then update the catalog pinned by [SOMEONE'S PC](https://github.com/furacca/someone-s-pc) for its next release

Use the matching paths if you changed `WORK_DIR`. Published releases and their assets are never replaced; a fix gets a new revision and release.

Release notes:

```markdown
Data obtained through [TCGDex](https://tcgdex.dev/)

## Source

| Field | Value |
| --- | --- |
| TCGdex source | <upstream tag or snapshot> |
| Snapshot base | <base release tag; omit this row for a tagged source> |
| Commit | `<full upstream commit SHA>` |
| Catalog revision | `<revision from catalog.lock.json>` |

## Changes

- <summary of make diff>

## Verify the download

Download the ZIP and its matching `catalog.lock.json` from this release.
Compare `sha256sum <release-tag>.zip` with the checksum below and `archive_sha256` in the lock.

**SHA-256:** `<archive_sha256 from catalog.lock.json>`
```

With the GitHub CLI, steps 3 and 4 become the commands below, with the notes saved to a file. Check the draft on GitHub before running the second one, which publishes it.

```sh
gh release create <release-tag> .cache/tcgdex/<release-tag>.zip catalog.lock.json \
  --target main --draft --title "<release title>" --notes-file <notes-file>
gh release edit <release-tag> --draft=false
```

## Update the compiler patch

If `prepare` reports that `server/compiler/utils/util.ts` changed:

1. Read the new `server/compiler/utils/util.ts` in `.cache/tcgdex/checkout`; the patch has not been applied at that point
2. Edit that file to make the three changes again: read the frozen image index, sort source enumeration and use the commit date when edit dates are missing
3. Replace everything from the first `diff --git` line of `tcgdex/util.ts.patch` with the output of `git -C .cache/tcgdex/checkout diff server/compiler/utils/util.ts`
4. Set `UTIL_TS_SHA256` to the checksum printed in the error, bump `BUILDER_VERSION` and rerun `make prepare`

Bump `BUILDER_VERSION` whenever a change to the patch, `prepare.sh` or the Go code alters the archive that is produced. Compare the upstream license with `cmp .cache/tcgdex/checkout/LICENSE tcgdex/LICENSE`; if it differs, copy the new license before publishing.
