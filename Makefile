GO      ?= go
BIN     ?= .
TOBBIE  := $(BIN)/tobbie
TOBBIE_MCP := $(BIN)/tobbie-mcp
CAPS    := cap_net_raw,cap_net_admin=eip

.PHONY: all build test vet clean setcap

all: build setcap

build: $(TOBBIE) $(TOBBIE_MCP)

$(TOBBIE): $(shell find cmd internal -name '*.go') go.mod go.sum
	$(GO) build -o $(TOBBIE) ./cmd/tobbie

$(TOBBIE_MCP): $(shell find cmd internal -name '*.go') go.mod go.sum
	$(GO) build -o $(TOBBIE_MCP) ./cmd/tobbie-mcp

setcap:
	sudo setcap $(CAPS) $(TOBBIE)
	sudo setcap $(CAPS) $(TOBBIE_MCP)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(TOBBIE) $(TOBBIE_MCP)