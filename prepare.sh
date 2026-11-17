#!/usr/bin/env bash
# Build a catalog archive from the pinned TCGdex inputs in tcgdex/pin.env.
#
# Usage:
#   ./prepare.sh capture-assets   Download a fresh CDN assets index into tcgdex/
#   ./prepare.sh prepare          Checkout, patch, compile offline, build, inspect
#   ./prepare.sh diff             Compare the candidate with the archive that
#                                 catalog.lock.json names
#
# Environment:
#   WORK_DIR          Scratch directory (default ./.cache/tcgdex)
#   RUNTIME           Container runtime (default podman)
#   PREVIOUS_CATALOG  Local copy of the archive catalog.lock.json names; diff
#                     downloads it from the lock's artifact_url otherwise

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INPUT_DIR="${REPO_DIR}/tcgdex"
PIN_FILE="${INPUT_DIR}/pin.env"
PATCH_FILE="${INPUT_DIR}/util.ts.patch"
BUILDER_FILE="${INPUT_DIR}/builder.Dockerfile"
FROZEN_ASSETS_INDEX="${INPUT_DIR}/assets-index.json.gz"
LOCK_FILE="${REPO_DIR}/catalog.lock.json"
RELEASES_URL="https://github.com/furacca/someone-s-pc-catalog/releases/download"
WORK_DIR="${WORK_DIR:-${REPO_DIR}/.cache/tcgdex}"
RUNTIME="${RUNTIME:-podman}"

fail() {
	printf 'prepare: %s\n' "$1" >&2
	exit 1
}

step() {
	printf '\n== %s\n' "$1"
}

# Paths below are passed to the container runtime, which needs them absolute.
mkdir -p "${WORK_DIR}"
WORK_DIR="$(cd "${WORK_DIR}" && pwd)"
CHECKOUT_DIR="${WORK_DIR}/checkout"
PRISTINE_ARCHIVE="${WORK_DIR}/upstream.tar.gz"
ASSETS_INDEX="${WORK_DIR}/assets-index.json"
GENERATED_DIR="${CHECKOUT_DIR}/server/generated"
CANDIDATE_LOCK="${WORK_DIR}/candidate.lock.json"
CANDIDATE_REPORT="${WORK_DIR}/candidate.report.txt"
CATALOGTOOL="${WORK_DIR}/catalogtool"

load_pin() {
	[ -f "${PIN_FILE}" ] || fail "missing ${PIN_FILE}"
	TCGDEX_TAG=""
	TCGDEX_BASE_TAG=""
	RELEASE_TAG=""
	# shellcheck disable=SC1090
	. "${PIN_FILE}"
	local name
	for name in TCGDEX_REPO TCGDEX_COMMIT UTIL_TS_SHA256 BUILDER_IMAGE BUILDER_VERSION ASSETS_INDEX_URL; do
		[ -n "${!name:-}" ] || fail "${name} is not set in pin.env"
	done
	[[ "${TCGDEX_COMMIT}" =~ ^[0-9a-f]{40}$ ]] || fail 'TCGDEX_COMMIT must be the full 40 character commit'
	[[ "${BUILDER_IMAGE}" == *@sha256:* ]] || fail 'BUILDER_IMAGE must be recorded by digest, not by tag'
	local source_tag="${TCGDEX_TAG:-${TCGDEX_BASE_TAG}}"
	[[ "${source_tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
		|| fail 'set TCGDEX_TAG for a release or TCGDEX_BASE_TAG for a snapshot to a tag such as v2.49.0'
	[ -z "${TCGDEX_TAG}" ] || [ -z "${TCGDEX_BASE_TAG}" ] \
		|| fail 'leave TCGDEX_BASE_TAG empty when TCGDEX_TAG is set'
	local base="someone-s-pc-catalog-${source_tag#v}"
	if [ -z "${TCGDEX_TAG}" ]; then
		base+="-${TCGDEX_COMMIT:0:12}"
	fi
	local pattern="^${base//./\\.}(-v([2-9]|[1-9][0-9]+))?$"
	RELEASE_TAG="${RELEASE_TAG:-${base}}"
	[[ "${RELEASE_TAG}" =~ ${pattern} ]] \
		|| fail "RELEASE_TAG must be ${base}, optionally followed by -v2, -v3 and so on"
}

# Builds catalogtool once per command, so later calls work from any directory.
build_catalogtool() {
	(cd "${REPO_DIR}" && go build -o "${CATALOGTOOL}" ./cmd/catalogtool) || fail 'building catalogtool failed'
}

locked_revision() {
	[ -f "${LOCK_FILE}" ] || fail "missing ${LOCK_FILE}"
	jq -er '.revision | select(type == "number" and . > 0 and floor == .)' "${LOCK_FILE}" \
		|| fail 'catalog.lock.json has no valid revision'
}

capture_assets() {
	step "capture ${ASSETS_INDEX_URL}"
	command -v jq >/dev/null 2>&1 || fail 'jq is not available'
	local staged="${WORK_DIR}/assets-index.json.part"
	curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
		--output "${staged}" "${ASSETS_INDEX_URL}" || fail 'capturing the assets index failed'
	jq empty "${staged}" || fail 'the captured assets index is not valid JSON'
	gzip -n -9 -c "${staged}" >"${FROZEN_ASSETS_INDEX}" || fail 'freezing the assets index failed'
	rm -f "${staged}"
	printf 'froze %s; prepare will use it. Commit it with the release.\n' "${FROZEN_ASSETS_INDEX#"${REPO_DIR}/"}"
}

restore_assets_index() {
	step 'unpack the frozen assets index'
	[ -f "${FROZEN_ASSETS_INDEX}" ] || fail "missing ${FROZEN_ASSETS_INDEX}; run capture-assets"
	gzip -dc "${FROZEN_ASSETS_INDEX}" >"${ASSETS_INDEX}" || fail 'unpacking the assets index failed'
}

checkout() {
	step "checkout ${TCGDEX_TAG:-snapshot} (${TCGDEX_COMMIT})"
	rm -rf "${CHECKOUT_DIR}"
	mkdir -p "${CHECKOUT_DIR}"
	git clone --quiet "${TCGDEX_REPO}" "${CHECKOUT_DIR}" || fail 'cloning the upstream repository failed'
	git -C "${CHECKOUT_DIR}" checkout --quiet "${TCGDEX_COMMIT}" || fail 'the pinned commit is not present in the clone'
	if [ -n "${TCGDEX_TAG}" ]; then
		local tagged
		tagged="$(git -C "${CHECKOUT_DIR}" rev-parse --verify "refs/tags/${TCGDEX_TAG}^{commit}" 2>/dev/null || true)"
		[ "${tagged}" = "${TCGDEX_COMMIT}" ] || fail "tag ${TCGDEX_TAG} does not point at ${TCGDEX_COMMIT}"
	else
		local base_commit
		base_commit="$(git -C "${CHECKOUT_DIR}" rev-parse --verify "refs/tags/${TCGDEX_BASE_TAG}^{commit}" 2>/dev/null)" \
			|| fail "base tag ${TCGDEX_BASE_TAG} is not present in the clone"
		[ "${base_commit}" != "${TCGDEX_COMMIT}" ] \
			|| fail "commit is exactly ${TCGDEX_BASE_TAG}; set TCGDEX_TAG and leave TCGDEX_BASE_TAG empty"
		git -C "${CHECKOUT_DIR}" merge-base --is-ancestor "${base_commit}" "${TCGDEX_COMMIT}" \
			|| fail "base tag ${TCGDEX_BASE_TAG} is not an ancestor of ${TCGDEX_COMMIT}"
	fi
}

apply_offline_patch() {
	step 'apply the offline compiler patch'
	local target="${CHECKOUT_DIR}/server/compiler/utils/util.ts"
	[ -f "${target}" ] || fail "the pinned checkout has no ${target#"${CHECKOUT_DIR}/"}"
	local actual
	actual="$(sha256sum "${target}" | cut -d' ' -f1)"
	[ "${actual}" = "${UTIL_TS_SHA256}" ] \
		|| fail "upstream changed ${target#"${CHECKOUT_DIR}/"} (sha256 ${actual}, expected ${UTIL_TS_SHA256}); review and re-record util.ts.patch"
	git -C "${CHECKOUT_DIR}" apply --whitespace=nowarn "${PATCH_FILE}" || fail 'applying the patch failed'
}

compile_offline() {
	step "compile with ${BUILDER_IMAGE}"
	command -v "${RUNTIME}" >/dev/null 2>&1 || fail "${RUNTIME} is not available"
	cp "${ASSETS_INDEX}" "${CHECKOUT_DIR}/spc-assets-index.json"
	cp "${INPUT_DIR}/compiler-contract.test.ts" "${CHECKOUT_DIR}/server/spc-compiler-contract.test.ts"
	local source_date_epoch
	source_date_epoch="$(git -C "${CHECKOUT_DIR}" show -s --format=%ct HEAD)"

	# The compiler reads commit dates through git, which the Bun image lacks.
	local builder
	builder="spc-catalog-builder:$(printf '%s' "${BUILDER_VERSION}" | tr -c 'A-Za-z0-9_.-' '-')"
	"${RUNTIME}" build --tag "${builder}" --build-arg "BASE=${BUILDER_IMAGE}" \
		--file "${BUILDER_FILE}" "${INPUT_DIR}" || fail 'building the builder image failed'

	# Dependencies are installed with the network, the compile runs without it.
	# Upstream installs the workspace root and the server package separately.
	"${RUNTIME}" run --rm --volume "${CHECKOUT_DIR}:/src" --workdir /src "${builder}" \
		sh -c 'bun install --frozen-lockfile && cd server && bun install --frozen-lockfile' \
		|| fail 'installing locked dependencies failed'

	"${RUNTIME}" run --rm --network none \
		--volume "${CHECKOUT_DIR}:/src" --workdir /src/server \
		--env SPC_ASSETS_INDEX=/src/spc-assets-index.json \
		--env "SOURCE_DATE_EPOCH=${source_date_epoch}" \
		"${builder}" \
		sh -c 'bun test ./spc-compiler-contract.test.ts && bun run compile' \
		|| fail 'the offline compile failed'

	[ -f "${GENERATED_DIR}/en/cards.json" ] || fail 'the compiler produced no English catalog'
	"${CATALOGTOOL}" canonicalize -generated "${GENERATED_DIR}" || fail 'normalizing the generated catalogs failed'
	printf 'generated catalogs: %s\n' "${GENERATED_DIR}"
}

snapshot_pristine() {
	step 'archive the upstream source with the compiled catalogs'
	# git archive reads HEAD, so the preserved source is the upstream revision as
	# published even though the working tree carries the patch. The compiled
	# catalogs are appended because the archive build checks that the source it
	# preserves is the input the projection read.
	local staged="${WORK_DIR}/upstream.tar"
	rm -f "${staged}"
	git -C "${CHECKOUT_DIR}" archive --format=tar --prefix='tcgdex/' HEAD >"${staged}" \
		|| fail 'archiving the checkout failed'
	local commit_date
	commit_date="$(git -C "${CHECKOUT_DIR}" show -s --format=%cI HEAD)"
	tar --append --file "${staged}" --directory "${CHECKOUT_DIR}" \
		--sort=name --owner=0 --group=0 --numeric-owner --mtime="${commit_date}" \
		--transform 's,^server/generated,tcgdex/server/generated,' \
		server/generated || fail 'adding the compiled catalogs to the snapshot failed'
	gzip -n -9 -c "${staged}" >"${PRISTINE_ARCHIVE}" || fail 'compressing the snapshot failed'
	rm -f "${staged}"
}

build_archive() {
	step 'build the candidate archive'
	"${CATALOGTOOL}" build \
		-source "${PRISTINE_ARCHIVE}" \
		-generated "${GENERATED_DIR}" \
		-assets-index "${ASSETS_INDEX}" \
		-out "${CANDIDATE}" \
		-lock "${CANDIDATE_LOCK}" \
		-report "${CANDIDATE_REPORT}" \
		-revision "${REVISION}" \
		-source-tag "${TCGDEX_TAG}" \
		-source-commit "${TCGDEX_COMMIT}" \
		-builder-version "${BUILDER_VERSION}" \
		-compiler-image "${BUILDER_IMAGE}" \
		-artifact-url "${RELEASES_URL}/${RELEASE_TAG}/${CANDIDATE##*/}" \
		|| fail 'building the archive failed'

	step 'inspect the candidate'
	"${CATALOGTOOL}" inspect -archive "${CANDIDATE}" -lock "${CANDIDATE_LOCK}" || fail 'inspecting the candidate failed'

	printf '\ncandidate: %s\n' "${CANDIDATE}"
	printf 'lock:      %s\n' "${CANDIDATE_LOCK}"
	printf 'report:    %s\n' "${CANDIDATE_REPORT}"
	printf '\nNext: ./prepare.sh diff, then review the report before releasing %s.\n' "${RELEASE_TAG}"
}

prepare() {
	command -v jq >/dev/null 2>&1 || fail 'jq is not available'
	# UTC never repeats an hour, unlike local time when daylight saving ends.
	REVISION="$(date -u +%Y%m%d%H%M)"
	local locked
	locked="$(locked_revision)"
	[ "${REVISION}" -gt "${locked}" ] \
		|| fail "revision ${REVISION} (UTC build time) is not greater than ${locked} in catalog.lock.json; wait until the next UTC minute or check the clock"
	local release_title
	if [ -z "${TCGDEX_TAG}" ]; then
		release_title="SOMEONE'S PC catalog for TCGdex ${TCGDEX_BASE_TAG#v} + ${TCGDEX_COMMIT:0:12} — revision ${REVISION}"
	else
		release_title="SOMEONE'S PC catalog for TCGdex ${TCGDEX_TAG#v} — revision ${REVISION}"
	fi
	CANDIDATE="${WORK_DIR}/${RELEASE_TAG}.zip"
	printf 'revision %s (UTC build time)\n' "${REVISION}"
	printf 'release tag %s\n' "${RELEASE_TAG}"
	printf 'release title %s\n' "${release_title}"
	[ -f "${BUILDER_FILE}" ] || fail "missing ${BUILDER_FILE}"
	build_catalogtool
	restore_assets_index
	checkout
	apply_offline_patch
	compile_offline
	snapshot_pristine
	build_archive
}

diff_against_lock() {
	step 'compare the candidate with the archive catalog.lock.json names'
	command -v jq >/dev/null 2>&1 || fail 'jq is not available'
	[ -f "${CANDIDATE_LOCK}" ] || fail "no candidate lock at ${CANDIDATE_LOCK}; run prepare first"
	# The lock retains the candidate filename between prepare and diff invocations.
	local archive_name
	archive_name="$(jq -er '.artifact_url | select(type == "string") | split("/") | last | select(startswith("someone-s-pc-catalog-") and endswith(".zip"))' "${CANDIDATE_LOCK}")" \
		|| fail 'candidate lock has no valid archive filename; run prepare first'
	CANDIDATE="${WORK_DIR}/${archive_name}"
	[ -f "${CANDIDATE}" ] || fail "no candidate at ${CANDIDATE}; run prepare first"
	build_catalogtool
	local previous="${WORK_DIR}/previous.zip"
	if [ -n "${PREVIOUS_CATALOG:-}" ]; then
		"${CATALOGTOOL}" fetch -lock "${LOCK_FILE}" -out "${previous}" -from "${PREVIOUS_CATALOG}" \
			|| fail 'PREVIOUS_CATALOG does not match catalog.lock.json'
	else
		"${CATALOGTOOL}" fetch -lock "${LOCK_FILE}" -out "${previous}" \
			|| fail 'downloading the previous archive failed; set PREVIOUS_CATALOG to a local copy'
	fi
	cp "${LOCK_FILE}" "${previous}.lock.json"
	"${CATALOGTOOL}" diff -old "${previous}" -new "${CANDIDATE}" -new-lock "${CANDIDATE_LOCK}"
}

main() {
	load_pin
	case "${1:-}" in
	capture-assets) capture_assets ;;
	prepare) prepare ;;
	diff) diff_against_lock ;;
	*)
		printf 'Usage: %s {capture-assets|prepare|diff}\n' "${0##*/}" >&2
		exit 2
		;;
	esac
}

main "$@"
