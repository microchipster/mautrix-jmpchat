# -- Build mautrix-jmpchat (with Go) --
FROM golang:1.27-alpine3.24 AS go-builder
RUN apk add --no-cache git ca-certificates build-base olm-dev zlib-dev opus-dev pkgconf

# Build tags. The default enables the optional SIP calling support (network.calls.enabled,
# off unless configured). "nolibopusfile" builds hraban/opus without the opusfile dependency.
# Set to an empty string for a minimal build without calling support.
ARG MAUTRIX_JMPCHAT_BUILD_TAGS="sipcalls nolibopusfile"

WORKDIR /build
# Copy all files needed for Go build
COPY go.mod go.sum *.yaml *.sh ./
COPY cmd/. cmd/.
COPY connector/. connector/.
COPY patched-xmpp/. patched-xmpp/.
COPY .git .git

RUN BUILD_TAGS="${MAUTRIX_JMPCHAT_BUILD_TAGS}" ./build-go.sh

# -- Run mautrix-jmpchat --
FROM alpine:3.24

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates bash yq-go olm tzdata opus

COPY --from=go-builder /build/mautrix-jmpchat /usr/bin/mautrix-jmpchat
COPY --from=go-builder /build/docker-run.sh /docker-run.sh
VOLUME /data

# SIP signaling and RTP media ports used when calls are enabled (network.calls).
# These are documentation only; publish them with `-p` when running the container.
EXPOSE 5060-5069/udp 5060-5069/tcp 40000-40100/udp

CMD ["/docker-run.sh"]
