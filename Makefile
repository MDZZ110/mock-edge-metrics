.PHONY: build
	docker build -t harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1 .
	docker push harbor.dev.thingsdao.com/edgewize/mock-edge-metrics:v0.0.1