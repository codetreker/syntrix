FROM golang:1.25.11-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /syntrix ./cmd/syntrix

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /syntrix /syntrix
ENV SYNTRIX_DATA_DIR=/data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/syntrix"]
CMD ["--standalone", "--config-dir", "/etc/syntrix"]
