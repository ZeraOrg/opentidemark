# syntax=docker/dockerfile:1
# SPDX-License-Identifier: Apache-2.0
#
# Multi-stage build (SPEC §6.1, §15.9, §17.2): node builds the web UI (skipped while web/ has no
# package.json), go cross-compiles a static binary for the target platform, and the runtime is
# distroless static as nonroot. The build stages run on the build platform, so a multi-arch build
# (linux/amd64 + linux/arm64, SPEC §17.6) needs no emulation for npm or the Go compiler.

FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/ ./
RUN if [ -f package.json ]; then npm ci && npm run build; else mkdir -p dist; fi

FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/ /tmp/web-dist/
RUN if [ -f /tmp/web-dist/index.html ]; then rm -rf internal/web/assets/dist && cp -R /tmp/web-dist internal/web/assets/dist; fi
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tidemark ./cmd/tidemark \
 && mkdir -p /out/licenses /out/data \
 && cp LICENSE NOTICE THIRD_PARTY_NOTICES.md /out/licenses/

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tidemark /usr/local/bin/tidemark
COPY --from=build /out/licenses/ /licenses/
COPY --from=build --chown=65532:65532 /out/data/ /data/
ENV TIDEMARK_DATA_DIR=/data
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080 5341 9200 4317 4318
HEALTHCHECK --interval=15s --timeout=6s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/tidemark", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/tidemark"]
CMD ["serve"]
