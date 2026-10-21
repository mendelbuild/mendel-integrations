# Built from the repository root:
#   docker build -f gmail-api/Dockerfile -t mendel-tool-gmail-api:dev .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY contract ./contract
COPY gmail-api ./gmail-api
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /gmail-api ./gmail-api

# A minimal base with CA certificates, for https://gmail.googleapis.com.
FROM gcr.io/distroless/static-debian12
COPY --from=build /gmail-api /gmail-api
ENTRYPOINT ["/gmail-api"]
