# Build from the repository root: docker build --platform linux/amd64 -f deploy/fly/Dockerfile .
FROM golang:1.26.10-bookworm AS build
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src/sidecar
COPY sidecar/go.mod sidecar/go.sum ./
RUN go mod download
COPY sidecar/ ./
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/trill ./cmd/trillctl

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates nftables locales util-linux \
    && localedef -i en_US -f UTF-8 en_US.UTF-8 \
    && groupadd --gid 10001 trill \
    && useradd --uid 10001 --gid 10001 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin trill \
    && install -d -m 0700 /data /run/trill \
    && install -d /usr/local/share/trill \
    && dpkg-query -W -f='${binary:Package}=${Version}\n' > /usr/local/share/trill/dpkg-versions.txt \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build --chmod=0555 /out/trill /usr/local/bin/trill
COPY --chmod=0555 deploy/fly/entrypoint.sh /usr/local/bin/trill-fly-entrypoint
ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 HOME=/nonexistent
EXPOSE 8080/tcp
# Root is used only by the fixed bootstrap; setpriv execs the service as UID/GID 10001.
USER 0:0
ENTRYPOINT ["/usr/local/bin/trill-fly-entrypoint"]
