# Memia core (app/) — development image.
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY app/ .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w -X main.version=dev-docker" -o /out/memia ./cmd/devbrain

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates postgresql-client \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/memia /usr/local/bin/memia
COPY deploy/docker/app-entrypoint.sh /usr/local/bin/app-entrypoint.sh
RUN useradd -r -u 10001 memia && chmod +x /usr/local/bin/app-entrypoint.sh
USER memia
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/app-entrypoint.sh"]
