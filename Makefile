all: build

FLAGS =
ENVVAR = GOOS=linux GOARCH=amd64 CGO_ENABLED=0
REGISTRY = registry.bizsaas.net
TAG = v0.2.0

deps:
	go get github.com/tools/godep

build: clean deps
	$(ENVVAR) godep  go build -o kube_event_watcher

test-unit: clean deps build
	$(ENVVAR) godep go test --race . $(FLAGS)

container: build
	docker build -t ${REGISTRY}/kube_event_watcher:$(TAG) .

push: container
	docker push ${REGISTRY}/kube_event_watcher:$(TAG)

clean:
	rm -f kube_event_watcher

.PHONY: all deps build test-unit container push clean
