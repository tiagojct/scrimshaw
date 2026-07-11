FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /scrimshaw ./cmd/scrimshaw

FROM scratch
COPY --from=build /scrimshaw /scrimshaw
VOLUME ["/data"]
ENV SCRIMSHAW_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/scrimshaw"]
