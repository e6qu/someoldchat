# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/sameoldchat ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sameoldchat /sameoldchat

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/sameoldchat"]
