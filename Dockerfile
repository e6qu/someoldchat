# syntax=mirror.gcr.io/docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89
# The Dockerfile frontend comes from Google's Docker Hub mirror and the Go image
# from the Amazon ECR Public copy of Docker's official images, both pinned to the
# digest Docker Hub serves: Docker Hub limits anonymous pulls per address, and
# the shared CI runners exhaust it.
FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build

ARG TARGETOS
ARG TARGETARCH
ARG RELEASE_REVISION

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test -n "$RELEASE_REVISION" && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.releaseRevision=$RELEASE_REVISION" -o /out/sameoldchat ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

COPY --from=build /out/sameoldchat /sameoldchat

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/sameoldchat"]
