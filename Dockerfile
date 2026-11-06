FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/immich-handoff ./cmd/immich-handoff
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/immich-handoff /immich-handoff
# Where the optional self-service store lives; owned by the unprivileged user the image runs as.
COPY --from=build --chown=nonroot:nonroot /data /data
VOLUME /data
EXPOSE 8080
# The image has no shell or curl, so the binary checks itself: it asks its own /healthz (on LISTEN_ADDR).
HEALTHCHECK --interval=30s --timeout=6s --start-period=40s --retries=3 CMD ["/immich-handoff", "--healthcheck"]
ENTRYPOINT ["/immich-handoff"]
