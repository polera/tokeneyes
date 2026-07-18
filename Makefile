BINARY := tokeneyes
PKG := ./cmd/tokeneyes
GOBIN := $(shell go env GOPATH)/bin

.PHONY: all build test checks staticcheck osv-scanner gosec install-tools clean

all: build

build:
	go build -o $(BINARY) $(PKG)

test:
	go test ./...

checks: staticcheck osv-scanner gosec

staticcheck: install-tools
	$(GOBIN)/staticcheck ./...

osv-scanner: install-tools
	$(GOBIN)/osv-scanner scan source -r .

gosec: install-tools
	$(GOBIN)/gosec ./...

install-tools:
	@command -v $(GOBIN)/staticcheck >/dev/null 2>&1 || \
		(echo "installing staticcheck..." && go install honnef.co/go/tools/cmd/staticcheck@latest)
	@$(GOBIN)/osv-scanner --version 2>/dev/null | grep -q 'version: 2\.' || \
		(echo "installing osv-scanner v2..." && go install github.com/google/osv-scanner/v2/cmd/osv-scanner@latest)
	@command -v $(GOBIN)/gosec >/dev/null 2>&1 || \
		(echo "installing gosec..." && go install github.com/securego/gosec/v2/cmd/gosec@latest)

clean:
	rm -f $(BINARY)
	go clean -testcache
