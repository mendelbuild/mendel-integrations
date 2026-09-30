# The Plausible wrapper: one static binary on a minimal base.
#
# Built from the repository root:
#   docker build -f plausible/Dockerfile -t mendel-tool-plausible:dev .
#
# The build stage runs on the builder's own platform and cross-compiles to
# linux/amd64, so nothing is compiled under emulation. The wrapper uses the
# standard library only; the contract module is copied beside it so the build reads
# no module from the network (Mendel's module is private).

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY contract ./contract
COPY plausible ./plausible
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/plausible ./plausible

# distroless/static carries CA certificates, which the HTTPS calls need, and
# nothing else.
FROM --platform=linux/amd64 gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/plausible /plausible
ENTRYPOINT ["/plausible"]
