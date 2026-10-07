# Build from the repository root:
#   docker build -f ayrshare/Dockerfile -t mendel-tool-ayrshare:dev .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY contract/ contract/
COPY ayrshare/ ayrshare/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/ayrshare ./ayrshare

FROM --platform=linux/amd64 gcr.io/distroless/static-debian12
COPY --from=build /out/ayrshare /ayrshare
ENTRYPOINT ["/ayrshare"]
