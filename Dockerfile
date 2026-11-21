# The base images are pinned by digest (the tag says what they are, the digest is what is built); Dependabot proposes new digests.
FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/immich-handoff ./cmd/immich-handoff
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/immich-handoff /immich-handoff
# Where the optional self-service store lives; owned by the unprivileged user the image runs as.
COPY --from=build --chown=nonroot:nonroot /data /data
VOLUME /data
EXPOSE 8080
# The image has no shell or curl, so the binary checks itself: it asks its own /healthz (on LISTEN_ADDR).
HEALTHCHECK --interval=30s --timeout=6s --start-period=40s --retries=3 CMD ["/immich-handoff", "--healthcheck"]
ENTRYPOINT ["/immich-handoff"]
