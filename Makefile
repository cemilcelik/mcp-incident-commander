.PHONY: all build test vet fmt clean

# Tüm paketleri derler ve testleri çalıştırır
all: vet test build

# Tüm paketleri derler
build:
	go build ./...

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
