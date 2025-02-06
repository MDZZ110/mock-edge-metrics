package main

import (
	"crypto/tls"
	"embed"
	"flag"
	"fmt"
	"github.com/MDZZ110/mock-edge-metrics/metrics"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

//go:embed node-metrics.txt
var mockNodeMetricsFile embed.FS

const charset = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func seededRandString(length int) string {
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}

func main() {
	var mu sync.Mutex
	failedCounter := 0
	successCounter := 0

	var interval int
	var cluster string
	var gatewayEndpoint string
	var nodeNum int
	var certFile string
	var keyFile string

	flag.IntVar(&interval, "interval", 5, "Interval in seconds")
	flag.StringVar(&cluster, "cluster", "", "Cluster name")
	flag.StringVar(&gatewayEndpoint, "gateway-endpoint", "https://172.31.18.9:30275", "Gateway endpoint")
	flag.IntVar(&nodeNum, "num", 1, "Number of nodes")
	//flag.StringVar(&certFile, "certFile", "/etc/ssl/whizard/server.crt", "Certificate file")
	//flag.StringVar(&keyFile, "keyFile", "/etc/ssl/whizard/server.key", "Certificate Key file")
	flag.StringVar(&certFile, "certFile", "/tmp/ssl/server.crt", "Certificate file")
	flag.StringVar(&keyFile, "keyFile", "/tmp/ssl/server.key", "Certificate Key file")
	flag.Parse()

	if cluster == "" {
		panic("Cluster name is required")
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan bool, 1)

	go func() {
		<-sigs
		fmt.Println("正在终止测试程序..")
		done <- true
	}()

	stopCh := make(chan struct{})

	for i := 0; i < nodeNum; i++ {
		go mockEdgeNode(i, interval, cluster, gatewayEndpoint, stopCh, &mu, &failedCounter, &successCounter, certFile, keyFile)
	}

	<-done
	stopCh <- struct{}{}
	fmt.Println("主线程退出")

}

func mockEdgeNode(num int, interval int, cluster string, endpoint string, stopCh chan struct{}, mu *sync.Mutex, failedCounter *int, successCounter *int, certFile, keyFile string) {
	duration := time.Duration(interval) * time.Second

	//targetUrl := fmt.Sprintf("%s/%s/api/v1/receive", endpoint, cluster)
	targetUrl := fmt.Sprintf("%s/push/%s", endpoint, cluster)
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

	if remoteWriteUrl.Scheme == "https" {
		httpRoundTripper.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}

		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			panic(err)
		}

		httpRoundTripper.TLSClientConfig.Certificates = []tls.Certificate{cert}
	}

	headers := map[string]string{}

	timeout := time.Duration(30 * time.Second)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "fake-node-" + seededRandString(5)
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

	rand.Seed(time.Now().UnixNano())
	//randomSecond := rand.Intn(interval)
	//time.Sleep(time.Duration(randomSecond) * time.Second)

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

			mu.Lock()
			if rCode != 0 {
				*failedCounter += 1
			} else {
				*successCounter += 1
			}
			fmt.Printf("当前执行总计 %d 个请求, 成功 %d, 失败 %d, 成功率 %.2f\n",
				*failedCounter+*successCounter, *successCounter, *failedCounter, float64(*successCounter)/float64(*failedCounter+*successCounter))
			mu.Unlock()

			fmt.Printf("当前执行总计 %d 个请求\n", *failedCounter+*successCounter)
		case <-stopCh:
			fmt.Printf("节点 [%d] 退出", num)
		}
	}
}
