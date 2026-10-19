compression?=true
# the build's version, reported by walletshield's /_/health
opt_version?=$(shell git describe --tags --always --dirty 2>/dev/null)
ldflags="-X github.com/ZeroKnowledgeNetwork/opt/common.CompressionEnabled=${compression} -X main.version=${opt_version}"

.PHONY: all app-walletshield genconfig http_proxy pki clean

all: app-walletshield http_proxy genconfig pki

app-walletshield:
	cd apps/walletshield && go build -trimpath -ldflags ${ldflags}

genconfig:
	cd genconfig/cmd/genconfig && go build

http_proxy:
	cd server_plugins/cbor_plugins/http_proxy/cmd/http_proxy && go build -trimpath -ldflags ${ldflags}

pki:
	make -C pki

clean:
	make -C pki clean
	rm -f \
		apps/walletshield/walletshield \
		genconfig/cmd/genconfig/genconfig \
		server_plugins/cbor_plugins/http_proxy/cmd/http_proxy/http_proxy
