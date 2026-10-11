FROM golang:1.25.11-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /console-server ./cmd/console-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /console-server /console-server
USER nonroot:nonroot
ENTRYPOINT ["/console-server"]
CMD ["--config", "/etc/console/config.yml"]
