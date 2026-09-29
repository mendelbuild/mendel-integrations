# Builds the plausible wrapper (package main, wrapperprotocol.WrapperRequest
# on stdin, wrapperprotocol.WrapperResponse on stdout). Cross-compiles for
# linux/amd64 onto a minimal base with CA certificates, since the wrapper
# talks HTTPS to a Plausible instance. See GUIDE.md "Writing one" (5).
FROM golang:1.26 AS build
WORKDIR /src
ENV GOFLAGS=-mod=vendor
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY plausible ./plausible
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/plausible ./plausible

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/plausible /plausible
ENTRYPOINT ["/plausible"]
