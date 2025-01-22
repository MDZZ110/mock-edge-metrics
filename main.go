package main

import (
	"embed"
	"flag"
	"fmt"
	"github.com/MDZZ110/mock-edge-metrics/metrics"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

//go:embed node-metrics.txt
var mockNodeMetricsFile embed.FS

func main() {

	var interval int
	var cluster string
	var gatewayEndpoint string
	flag.IntVar(&interval, "interval", 60, "Interval in seconds")
	flag.StringVar(&cluster, "cluster", "", "Cluster name")
	flag.StringVar(&gatewayEndpoint, "gateway-endpoint", "http://gateway-whizard-operated.kubesphere-monitoring-system.svc.cluster.local:9090", "Gateway endpoint")
	flag.Parse()

	if cluster == "" {
		panic("Cluster name is required")
	}

	duration := time.Duration(interval) * time.Second

	targetUrl := fmt.Sprintf("%s/%s/api/v1/receive", gatewayEndpoint, cluster)
	remoteWriteUrl, err := url.Parse(targetUrl)
	if err != nil {
		panic(err)
	}

	httpRoundTripper := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	headers := map[string]string{}

	timeout := time.Duration(30 * time.Second)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "mock-node"
		fmt.Printf("Environment variable NODE_NAME not set. Using default %s\n", nodeName)
	}

	labels := map[string]string{
		"node": nodeName,
		"app":  "fluentbit",
		"job":  "kubeedge",
	}

	fmt.Printf("Parsing metrics file %s\n", "node-metrics.txt")
	mockMetricsData, err := mockNodeMetricsFile.ReadFile("node-metrics.txt")
	if err != nil {
		panic(err)
	}

	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			fmt.Println("发送节点模拟监控数据: ", time.Now().Format(time.RFC3339))
			rCode := metrics.PushMetrics(remoteWriteUrl, httpRoundTripper, headers, timeout, labels, mockMetricsData)
			if rCode != 0 {
				fmt.Printf("推送模拟数据失败: %d\n", rCode)
			}
		}
	}

}
