FROM golang:1.26.4-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o /out/portlight ./cmd/portlight

FROM alpine:3.22

RUN adduser -D -H -s /sbin/nologin portlight
COPY --from=build /out/portlight /usr/local/bin/portlight
USER portlight

ENTRYPOINT ["portlight"]
