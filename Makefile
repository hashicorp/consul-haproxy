VERSION = "0.2.0"
DEPS = $(go list -f '{{range .TestImports}}{{.}} {{end}}' ./...)

all: deps
	@mkdir -p bin/
	go build -o bin/consul-haproxy

deps:
	go get -d -v ./...
	echo $(DEPS) | xargs -n1 go get -d

test: deps
	go list ./... | xargs -n1 go test

release: deps test
	@rm -rf build/
	@mkdir -p build
	gox \
		-os="windows darwin linux netbsd freebsd openbsd netbsd" \
		-output="build/{{.Dir}}_$(VERSION)_{{.OS}}_{{.Arch}}/consul-haproxy"
	@mkdir -p build/tgz
	(cd build && ls | xargs -I {} tar -zcvf tgz/{}.tar.gz {})

.PHONY: all deps test
