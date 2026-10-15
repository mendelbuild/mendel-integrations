# Built from the repository root:
#   docker build -f mailgun/Dockerfile -t mendel-tool-mailgun:dev .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY contract ./contract
COPY mailgun ./mailgun
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/mailgun ./mailgun

# distroless/static carries CA certificates and nothing else.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/mailgun /mailgun
ENTRYPOINT ["/mailgun"]
