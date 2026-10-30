# -- Build mautrix-jmpchat (with Go) --
FROM golang:1.27.3-alpine3.24 AS go-builder
RUN apk add --no-cache git ca-certificates build-base opus-dev pkgconf

# The image is always built with pure-Go Matrix E2BE ("goolm", no libolm) and
# SIP/MatrixRTC calling support ("sipcalls", "nolibopusfile": hraban/opus
# without opusfile). There is one image variant; calling stays opt-in per
# account via the `sip-login` management-room command and can be disabled
# bridge-wide with `network.calls.enabled: false`.

WORKDIR /build
# Copy all files needed for Go build
COPY go.mod go.sum *.yaml *.sh ./
COPY cmd/. cmd/.
COPY connector/. connector/.
COPY pkg/. pkg/.
COPY patched-xmpp/. patched-xmpp/.
# third_party/ carries locally patched Go modules (see the replace directives in go.mod)
COPY third_party/. third_party/.
COPY .git .git

RUN BUILD_TAGS="goolm sipcalls nolibopusfile" ./build-go.sh

# -- Run mautrix-jmpchat --
FROM alpine:3.24

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates bash yq-go tzdata opus

COPY --from=go-builder /build/mautrix-jmpchat /usr/bin/mautrix-jmpchat
COPY --from=go-builder /build/docker-run.sh /docker-run.sh
VOLUME /data

# SIP signaling and RTP ports used by the calling support (opt-in per account).
# These are documentation only; publish them with `-p` when running the container.
EXPOSE 5060-5069/udp 5060-5069/tcp 40000-40100/udp

CMD ["/docker-run.sh"]
