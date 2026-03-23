all: fmt lint build test

ubuntu-deps:
	sudo apt-get update && sudo apt-get install -y --no-install-recommends -o APT::Install-Suggests=0 \
		fonts-dejavu-core \
		libasound2t64 \
		libatk-bridge2.0-0 \
		libatk1.0-0 \
		libcairo2 \
		libcups2 \
		libxcb-cursor0 \
		libdbus-1-3 \
		libdrm2 \
		libegl1 \
		libexpat1 \
		libfontconfig1 \
		libfreetype6 \
		libgbm1 \
		libglib2.0-0 \
		libnspr4 \
		libnss3 \
		libopengl0 \
		libpango-1.0-0 \
		libx11-6 \
		libxcb1 \
		libxcomposite1 \
		libxdamage1 \
		libxfixes3 \
		libxkbcommon0 \
		libxrandr2 \
		ghostscript \
		sqlite3 \
		ffmpeg \
		groff \
		pandoc \
		wget \
		unar \
		libheif-plugin-aomdec \
		libheif-plugin-aomenc \
		libheif-plugin-libde265
	sudo -v && wget -qO- https://imagemagick.org/archive/binaries/magick -O /usr/local/bin/magick && sudo chmod +x /usr/local/bin/magick
	sudo -v && wget -qO- https://download.calibre-ebook.com/linux-installer.sh | sudo sh /dev/stdin

macos-deps:
	-brew install --formula ffmpeg pandoc sqlite imagemagick unar || true
	-brew install --cask calibre

windows-deps:
	choco install calibre ffmpeg-full sqlite imagemagick unar --no-progress --stop-on-first-failure

go-deps:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install mvdan.cc/gofumpt@latest
	go install github.com/daixiang0/gci@latest
	go install gotest.tools/gotestsum@latest

deps-update:
	go get -u ./...
	go mod tidy

build:
	CGO_ENABLED=1 go build -tags fts5 -o shrink ./cmd/shrink

test:
	CGO_ENABLED=1 gotestsum --format pkgname-and-test-fails -- -tags fts5 ./...

cover:
	CGO_ENABLED=1 gotestsum --format pkgname-and-test-fails -- -tags fts5 ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | awk '{n=split($$NF,a,"%%"); if (a[1] < 85) print $$0}' | sort -k3 -n

fmt:
	gofmt -s -w -e .
	-goimports -w -e .
	-gofumpt -w .
	-gci write .
	go fix ./...

lint:
	-staticcheck -tags fts5 ./...
	go vet -tags fts5 ./...

install:
	CGO_ENABLED=1 go install -tags fts5 ./cmd/shrink
