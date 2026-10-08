TAGS   := novulkan,nox11
PREFIX ?= $(HOME)/.local

.PHONY: build run install uninstall vet

build:
	go build -tags $(TAGS) -trimpath -ldflags "-s -w" -o bin/omawhats .

run: build
	./bin/omawhats

vet:
	go vet -tags $(TAGS) ./...

install: build
	install -Dm755 bin/omawhats $(PREFIX)/bin/omawhats
	install -Dm644 omawhats.desktop $(PREFIX)/share/applications/omawhats.desktop

uninstall:
	rm -f $(PREFIX)/bin/omawhats $(PREFIX)/share/applications/omawhats.desktop
