.PHONY: docker-build
docker-build:
	docker build -t harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1 .
	docker push harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1

.PHONY: build
build:
	GOOS=linux GOARCH=amd64 go build -a -o nodeMetricsMocker main.go