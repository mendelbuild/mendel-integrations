# Builds from the repository root (the module's vendor/ and go.mod live
# there), producing the static binary registered as this wrapper's
# ENTRYPOINT. Build with, from the repository root:
#
#   docker build -f tavily/Dockerfile -t mendel-tool-tavily:dev .
#
# matching wrapper.json's "image". GUIDE.md ("Writing one" §5): compiles
# natively, cross-compiles for linux/amd64 (the only architecture a
# project's cluster runs a Job on), onto a minimal base carrying CA
# certificates (Tavily's API is HTTPS-only), with ENTRYPOINT in exec form.

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY tavily ./tavily
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -mod=vendor -o /out/tavily ./tavily

# distroless/static carries /etc/ssl/certs/ca-certificates.crt and nothing
# else: no shell, no package manager, nothing for a wrapper to need beyond
# outbound HTTPS to api.tavily.com.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tavily /tavily
ENTRYPOINT ["/tavily"]
