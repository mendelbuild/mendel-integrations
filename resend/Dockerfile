# Built from the repository root: docker build -f resend/Dockerfile -t mendel-tool-resend:dev .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod ./
COPY contract ./contract
COPY resend ./resend
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/resend ./resend

FROM alpine:3
RUN apk add --no-cache ca-certificates
COPY --from=build /out/resend /resend
ENTRYPOINT ["/resend"]
