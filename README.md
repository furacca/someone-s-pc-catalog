# SOMEONE'S PC catalog

Pokémon card catalogs for [SOMEONE'S PC](https://github.com/furacca/someone-s-pc). Each archive contains card data, printings, translations and image addresses, together with its upstream source. Images are downloaded separately by the application.

**Data obtained through TCGDex [https://tcgdex.dev/](https://tcgdex.dev/)**

If you want to support their work, you can [sponsor TCGdex on GitHub](https://github.com/sponsors/tcgdex).

## Download a catalog

Download the ZIP and its matching `catalog.lock.json` from the same [GitHub release](https://github.com/furacca/someone-s-pc-catalog/releases).

| File | Purpose |
| --- | --- |
| `<release-tag>.zip` | Catalog data and preserved upstream source |
| `catalog.lock.json` | Archive revision, download URL, size and sha256 |

| Example filename | TCGdex source |
| --- | --- |
| `someone-s-pc-catalog-tcgdex-2.49.0.zip` | TCGdex 2.49.0 |
| `someone-s-pc-catalog-tcgdex-2.49.0-v2.zip` | Same TCGdex 2.49.0 commit, with a correction to our catalog |
| `someone-s-pc-catalog-202611161100-snapshot.zip` | A fixed TCGdex commit without a release tag |

The ZIP filename is the release tag followed by `.zip`. Tags start with `someone-s-pc-catalog-`; `-v2`, `-v3` and so on count corrections to our catalog using the same TCGdex source commit.

Published releases are never changed. Each new archive has a numeric UTC build revision in `YYYYMMDDHHMM` format. SOMEONE'S PC refuses older revisions, regardless of the release name; snapshots use the same ordering.

## Verify the download

Use the filename of the release you downloaded, for example:

```sh
sha256sum someone-s-pc-catalog-tcgdex-2.49.0.zip
```

Compare the result with `archive_sha256` in the downloaded lock. The file size must match `archive_bytes`. Keep the archive and its matching lock together.

## How SOMEONE'S PC uses the catalog

SOMEONE'S PC pins a catalog archive and embeds it when building its release executable. A prebuilt application already contains its selected catalog.

This repository provides the versioned archives and preparation recipe so the catalog's contents and provenance can be inspected and the catalog can be rebuilt.

For instructions on compiling the application, see the [SOMEONE'S PC repository](https://github.com/furacca/someone-s-pc).

## Build your own catalog

See [Building and publishing catalogs](docs/building.md) to select an upstream release or commit, build an archive and publish it.

## License

The code is under the MIT license in [LICENSE](LICENSE). TCGdex data is covered by the upstream MIT license in [tcgdex/LICENSE](tcgdex/LICENSE); each archive preserves that source and license. The frozen image index and compiler patch are also TCGdex material.

Pokémon and its trademarks belong to Nintendo, The Pokémon Company and Game Freak. This project is not affiliated with them or with TCGdex.
