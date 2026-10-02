# Built from the repository root:
#   docker build -f mailchimp/Dockerfile -t mendel-tool-mailchimp:dev .
#
# Compiles natively, and cross-compiles for linux/amd64 (the Dockerfile's own
# platform is whatever the daemon builds for; GOARCH/GOOS below pin the
# binary to the image's, which is linux/amd64) onto a minimal base that
# carries CA certificates, since the wrapper speaks HTTPS to Mailchimp's API.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY contract/ ./contract/
COPY mailchimp/ ./mailchimp/
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -o /mailchimp ./mailchimp

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /mailchimp /mailchimp
ENTRYPOINT ["/mailchimp"]
