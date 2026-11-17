# AGENTS.md

Instructions for agents working on this repository. Procedures are in [docs/building.md](docs/building.md).

## Scope

- Keep the tooling simple: no CI, automation or new dependency unless asked
- If you change the archive format, check compatibility with the reader in `../someone-s-pc/internal/catalogdata` and update it when needed

## Rules

- Never commit archives (`*.zip`) or `.cache/`
- Never change, replace or delete a published release or its assets
- Never write a revision by hand; `prepare.sh` sets it
- Follow the release tag rule in the README
- Credit TCGDex in the README and every release's notes, with a clickable link to https://tcgdex.dev/
- Run `make prepare` or `make capture-assets` only when asked
- Do not commit, tag, push or publish unless asked

## Verification

- Go changes: `make test`
- `prepare.sh` changes: `shellcheck prepare.sh`, plus the changed error paths without a full build
- Documentation changes: check relative links and `git diff --check`
- Say what was not run

## Context

- Never print raw TCGdex JSON, the image index or archive contents; use `catalogtool inspect` or extract fields with `jq`
- Filter long command output and read narrow ranges of large files

## Style

- Write short, direct English; avoid filler, repetition and inflated language
- Use tables for reference data
- No full stops at the end of bullet items
- No emoji
- Comments explain non-obvious behavior, not syntax
