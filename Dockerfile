# SPDX-License-Identifier: AGPL-3.0-or-later
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags "-buildid= -s -w -X main.buildVersion=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/neptide ./cmd/neptide
RUN apk add --no-cache ca-certificates

# Nothing but the binary and CA certificates.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/neptide /neptide
# Unprivileged. Socket access comes from the docker group: run with
# user: "65532:<docker gid>" (see compose.yaml).
USER 65532:65532
# Where mounted docker-credential-* helpers are found.
ENV PATH=/usr/local/bin
LABEL neptide.self=true
ENTRYPOINT ["/neptide"]
