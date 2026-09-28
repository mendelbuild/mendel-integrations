# The Mastodon wrapper: one static binary on a minimal base.
#
# Built from the repository root:
#   docker build -f mastodon/Dockerfile -t mendel-tool-mastodon:dev .
#
# The build stage runs on the builder's own platform and cross-compiles to
# linux/amd64, so nothing is compiled under emulation. The vendored protocol
# is copied so the build reads no module from the network.

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY mastodon ./mastodon
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/mastodon ./mastodon

# distroless/static carries CA certificates, which the HTTPS calls need, and
# nothing else.
FROM --platform=linux/amd64 gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mastodon /mastodon
ENTRYPOINT ["/mastodon"]
