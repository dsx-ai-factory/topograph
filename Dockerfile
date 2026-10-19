# Base images are pinned by digest; Dependabot's docker ecosystem
# (.github/dependabot.yml) keeps the tag and digest pairs current.
FROM golang:1.27.2@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS builder

WORKDIR /go/src/github.com/dsx-ai-factory/topograph
COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN make build-${TARGETOS}-${TARGETARCH}

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk add --no-cache rdma-core

COPY --from=builder /go/src/github.com/dsx-ai-factory/topograph/bin/* /usr/local/bin/

LABEL org.opencontainers.image.documentation="https://github.com/dsx-ai-factory/topograph/blob/main/docs/overview.md" \
    org.opencontainers.image.authors="NVIDIA CORPORATION" \
    org.opencontainers.image.vendor="NVIDIA"
