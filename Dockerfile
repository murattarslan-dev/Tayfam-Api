# syntax=docker/dockerfile:1
#
# Tüm servisler için tek Dockerfile; servis SERVICE argümanıyla seçilir:
#   docker build --build-arg SERVICE=gateway -t tayfam-gateway .

FROM golang:1.27-alpine AS build
ARG SERVICE
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN test -n "$SERVICE" && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/murattarslan-dev/tayfam-api/internal/platform/buildinfo.Version=${VERSION}" \
      -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/app"]
