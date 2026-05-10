APP_NAME := claw
CMD_DIR := ./cmd/claw
BIN := ./claw

.PHONY: help build run serve install uninstall restart reload test tidy clean

help:
	@echo "Available targets:"
	@echo "  build      Build binary to $(BIN)"
	@echo "  run        Run interactive client"
	@echo "  serve      Start daemon in foreground"
	@echo "  install    Register startup service"
	@echo "  uninstall  Remove startup service"
	@echo "  restart    Restart daemon service"
	@echo "  reload     Reload prompts/config in daemon"
	@echo "  test       Run all tests"
	@echo "  tidy       Sync go modules"
	@echo "  clean      Remove built binary"

build:
	go build -o $(BIN) $(CMD_DIR)

run: build
	$(BIN)

serve: build
	$(BIN) serve

install: build
	$(BIN) install

uninstall: build
	$(BIN) uninstall

restart: build
	$(BIN) restart

reload: build
	$(BIN) reload

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)
