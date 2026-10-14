# -- Build mautrix-jmpchat (with Go) --
FROM golang:1.27-alpine3.23 AS go-builder
RUN apk add --no-cache git ca-certificates build-base olm-dev zlib-dev

WORKDIR /build
# Copy all files needed for Go build
COPY go.mod go.sum *.yaml *.sh ./
COPY cmd/. cmd/.
COPY connector/. connector/.
COPY patched-xmpp/. patched-xmpp/.
COPY .git .git

RUN ./build-go.sh

# -- Run mautrix-jmpchat --
FROM alpine:3.23

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates bash yq-go olm tzdata

COPY --from=go-builder /build/mautrix-jmpchat /usr/bin/mautrix-jmpchat
COPY --from=go-builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
