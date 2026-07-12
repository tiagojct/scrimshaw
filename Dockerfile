FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /scrimshaw ./cmd/scrimshaw

FROM scratch
# CA certificates so the app can fetch feeds, pages, and images over HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /scrimshaw /scrimshaw
VOLUME ["/data"]
ENV SCRIMSHAW_DATA_DIR=/data
EXPOSE 8080
# The image has no shell, so the binary is its own health probe.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/scrimshaw", "-healthcheck"]
ENTRYPOINT ["/scrimshaw"]
