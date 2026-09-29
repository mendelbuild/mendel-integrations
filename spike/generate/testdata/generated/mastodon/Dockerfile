# Built with this repository's root as the context (`docker build -f
# mastodon/Dockerfile .`), since the module's go.mod, go.sum and vendor/ live
# there and this wrapper imports the vendored wrapperprotocol.
#
# Builds statically for linux/amd64 regardless of the host, per GUIDE.md
# "Writing one" §5: a wrapper's image must run in a project's cluster, which
# is amd64, whatever the admin's own machine is.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY mastodon ./mastodon
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=vendor -trimpath -o /out/mastodon ./mastodon

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mastodon /mastodon
ENTRYPOINT ["/mastodon"]
