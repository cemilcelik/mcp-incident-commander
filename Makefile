.PHONY: all build build-lambda test vet fmt clean

# Tüm paketleri derler ve testleri çalıştırır
all: vet test build

# Tüm paketleri derler
build:
	go build ./...

# AWS Lambda provided.al2023 ARM64 için bootstrap ikilisini derler ve zip paketi oluşturur
build-lambda:
	mkdir -p bin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -ldflags="-s -w" -o bin/bootstrap ./cmd/mcp-server
	cd bin && zip -q mcp-server-lambda-arm64.zip bootstrap
	rm -f bin/bootstrap

# Yarış durumu (race detector) aktif olarak tüm unit testleri çalıştırır
test:
	go test -v -race ./...

# Statik kod analizi çalıştırır
vet:
	go vet ./...

# Kod formatlamasını kontrol eder ve düzenler
fmt:
	gofmt -s -w .

# Derleme çıktılarını temizler
clean:
	rm -rf bin/
