# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /opens3 ./cmd/opens3

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /opens3 /opens3
VOLUME /data
EXPOSE 9000
USER nonroot
ENTRYPOINT ["/opens3"]
CMD ["server", "--root", "/data", "--address", ":9000"]
