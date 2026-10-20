FROM ubuntu:latest AS base

# Install run-time dependencies
RUN \
  --mount=type=cache,target=/var/cache/apt \
  --mount=type=cache,target=/var/lib/apt \
  apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  && rm -rf /var/lib/apt/lists/*


FROM base AS builder

ARG ENABLE_COMPRESSION=false
# the version walletshield reports: the build context has no .git to stamp it
# from, e.g. --build-arg VERSION=$(git describe --tags --always --dirty)
ARG VERSION=
ARG KATZENPOST_DIR=/src/katzenpost
# must satisfy the `go` directive of both go.mod files (this repo's is the
# higher of the two), otherwise the toolchain is downloaded at build time
ARG VERSION_GO=1.26.9

ENV GOROOT=/usr/local/go
ENV PATH=$GOROOT/bin:$PATH
ENV GOCACHE=/root/.cache/go-build
# one -ldflags: when repeated, only the last applies
ENV GO_LDFLAGS="-buildid= -X=github.com/ZeroKnowledgeNetwork/opt/common.CompressionEnabled=${ENABLE_COMPRESSION} -X=main.version=${VERSION}"

# Install build dependencies
RUN \
  --mount=type=cache,target=/var/cache/apt \
  --mount=type=cache,target=/var/lib/apt \
  apt-get update && apt-get install -y --no-install-recommends \
  build-essential \
  git \
  wget

# Install Go
RUN f=go${VERSION_GO}.linux-amd64.tar.gz \
  && wget https://dl.google.com/go/${f} \
  && tar -C /usr/local -xzf ${f} \
  && rm ${f}

# Copy the project source
COPY . /src

# Build Katzenpost components
RUN --mount=type=cache,target="${GOCACHE}" \
  mkdir /dest \
  # clone opt-specific katzenpost branch, if one does not exist
  && if [ ! -d "${KATZENPOST_DIR}" ]; then make katzenpost_dir=${KATZENPOST_DIR} -C /src/docker clone-katzenpost ; fi \
  # a function to build and move the binary
  && build() { cd ${KATZENPOST_DIR}/$1 ; b=$(basename $1) ; go build -trimpath -ldflags="-buildid=" ; chmod u+x $b ; mv $b /dest/$2; } \
  # clients
  && build cmd/fetch fetch \
  && build cmd/kpclientd kpclientd \
  && build cmd/ping ping \
  # servers
  && build cmd/dirauth dirauth \
  && build cmd/server server \
  ## services
  && build cmd/courier courier \
  && build cmd/echo-plugin echo-plugin \
  && build cmd/replica replica

# Build ZKN mix network components
RUN --mount=type=cache,target="${GOCACHE}" \
  # a function to build and move the binary
  build() { cd /src/$1 ; b=$(basename $1) ; go build -trimpath -ldflags="${GO_LDFLAGS}" ; chmod u+x $b ; mv $b /dest/$2; } \
  # pki
  && build pki/cmd/pki pki \
  # genconfig
  && build genconfig/cmd/genconfig genconfig \
  # servicenode plugins
  && build server_plugins/cbor_plugins/rpc_proxy/cmd/rpc_proxy rpc_proxy \
  # clients
  && build apps/walletshield walletshield


FROM base AS node

ARG DIR_BIN=/opt/zkn

COPY --from=builder /dest ${DIR_BIN}

CMD ["/bin/bash"]
