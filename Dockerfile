# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$VERSION" -o /out/donate ./cmd/donate

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 donate \
    && adduser -S -D -H -u 10001 -G donate donate \
    && mkdir -p /var/lib/donate \
    && chown donate:donate /var/lib/donate \
    && chmod 0700 /var/lib/donate
COPY --from=build /out/donate /usr/local/bin/donate
USER 10001:10001
WORKDIR /var/lib/donate
ENV DONATE_ADDR=0.0.0.0:8080 \
    DONATE_DATA_DIR=/var/lib/donate \
    DONATE_PUBLIC_URL=http://localhost:8080
VOLUME ["/var/lib/donate"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/donate"]
CMD ["serve"]
