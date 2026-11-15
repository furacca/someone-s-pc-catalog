# Builder image for the offline TCGdex compile.
#
# BASE is the digest pinned in pin.env as BUILDER_IMAGE. Upstream compiles with
# oven/bun and reads commit dates through git, which that image does not carry,
# so this layer adds git and nothing else. BUILDER_VERSION in pin.env
# identifies this recipe; bump it whenever this file changes.
ARG BASE
FROM ${BASE}
RUN apk add --no-cache git \
	&& git config --global safe.directory '*'
