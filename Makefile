.PHONY: all
all: clean replace-word

replace-word: replace-word.go
	@echo ">> Compiling..."
	go build $<

.PHONY: clean
clean:
	@echo ">> Cleaning up..."
	rm -f replace-word

.PHONY: deps
deps:
	@echo ">> Resolving dependencies..."
	go mod download
	go mod tidy

.PHONY: deps-update
deps-update:
	@echo ">> Updating dependencies..."
	go get -d -u -t ./...
	@make deps
