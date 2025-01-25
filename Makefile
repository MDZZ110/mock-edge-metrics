.PHONY: docker-build
docker-build:
	docker build -t harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1 .
	docker push harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1

.PHONY: buildx-images
	docker buildx build --platform="linux/amd64,linux/arm64" -t harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1 --push .

.PHONY: build
build:
	GOOS=linux GOARCH=amd64 go build -a -o bin/nodeMetricsMocker main.go