# -- Build mautrix-jmpchat (with Go) --
FROM golang:1-alpine3.23 AS go-builder
RUN apk add --no-cache git ca-certificates build-base olm-dev zlib-dev

WORKDIR /build
# Copy all files needed for Go build
COPY *.go go.* *.yaml *.sh ./
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY connector ./connector/
COPY .git .git

ENV LIBRARY_PATH=.
RUN ./build-go.sh
#RUN go build -o /jmpchat .

# -- Run mautrix-jmpchat --
FROM alpine:3.23

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates bash jq curl yq-go olm tzdata

COPY --from=go-builder /build/mautrix-jmpchat /usr/bin/mautrix-jmpchat
COPY --from=go-builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
