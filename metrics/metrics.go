package metrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/prometheus/prompb"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/golang/snappy"
	dto "github.com/prometheus/client_model/go"
	config_util "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"

	"github.com/prometheus/prometheus/storage/remote"
)

var MetricMetadataTypeValue = map[string]int32{
	"UNKNOWN":        0,
	"COUNTER":        1,
	"GAUGE":          2,
	"HISTOGRAM":      3,
	"GAUGEHISTOGRAM": 4,
	"SUMMARY":        5,
	"INFO":           6,
	"STATESET":       7,
}

const (
	sumStr          = "_sum"
	countStr        = "_count"
	bucketStr       = "_bucket"
	successExitCode = 0
	failureExitCode = 1
	// Exit code 3 is used for "one or more lint issues detected".
	lintErrExitCode = 3

	lintOptionAll                   = "all"
	lintOptionDuplicateRules        = "duplicate-rules"
	lintOptionTooLongScrapeInterval = "too-long-scrape-interval"
	lintOptionNone                  = "none"
	checkHealth                     = "/-/healthy"
	checkReadiness                  = "/-/ready"
)

// PushMetrics to a prometheus remote write (for testing purpose only).
func PushMetrics(url *url.URL, roundTripper http.RoundTripper, headers map[string]string, timeout time.Duration, labels map[string]string, data []byte) int {
	addressURL, err := url.Parse(url.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return failureExitCode
	}

	// build remote write client
	writeClient, err := remote.NewWriteClient("remote-write", &remote.ClientConfig{
		URL:     &config_util.URL{URL: addressURL},
		Timeout: model.Duration(timeout),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return failureExitCode
	}

	// set custom tls config from httpConfigFilePath
	// set custom headers to every request
	client, ok := writeClient.(*remote.Client)
	if !ok {
		fmt.Fprintln(os.Stderr, fmt.Errorf("unexpected type %T", writeClient))
		return failureExitCode
	}
	client.Client.Transport = &setHeadersTransport{
		RoundTripper: roundTripper,
		headers:      headers,
	}

	var failed bool

	if parseAndPushMetrics(client, data, labels) {
		fmt.Printf("  SUCCESS: mock metrics file pushed to remote write.\n")
	} else {
		fmt.Printf("  FAILURE: mock metrics file pushed to remote write.\n")
		failed = true
	}

	if failed {
		return failureExitCode
	}

	return successExitCode
}

func MetricFamiliesToWriteRequest(mf map[string]*dto.MetricFamily, extraLabels map[string]string) (*prompb.WriteRequest, error) {
	wr := &prompb.WriteRequest{}

	// build metric list
	sortedMetricNames := make([]string, 0, len(mf))
	for metric := range mf {
		sortedMetricNames = append(sortedMetricNames, metric)
	}
	// sort metrics name in lexicographical order
	sort.Strings(sortedMetricNames)

	for _, metricName := range sortedMetricNames {
		// Set metadata writerequest
		mtype := MetricMetadataTypeValue[mf[metricName].Type.String()]
		metadata := prompb.MetricMetadata{
			MetricFamilyName: mf[metricName].GetName(),
			Type:             prompb.MetricMetadata_MetricType(mtype),
			Help:             mf[metricName].GetHelp(),
		}
		wr.Metadata = append(wr.Metadata, metadata)

		for _, metric := range mf[metricName].Metric {
			labels := makeLabelsMap(metric, metricName, extraLabels)
			if err := makeTimeseries(wr, labels, metric); err != nil {
				return wr, err
			}
		}
	}
	return wr, nil
}

func MetricTextToWriteRequest(input io.Reader, labels map[string]string) (*prompb.WriteRequest, error) {
	var parser expfmt.TextParser
	mf, err := parser.TextToMetricFamilies(input)
	if err != nil {
		return nil, err
	}
	return MetricFamiliesToWriteRequest(mf, labels)
}

// TODO(bwplotka): Add PRW 2.0 support.
func parseAndPushMetrics(client *remote.Client, data []byte, labels map[string]string) bool {
	metricsData, err := MetricTextToWriteRequest(bytes.NewReader(data), labels)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  FAILED:", err)
		return false
	}

	raw, err := metricsData.Marshal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "  FAILED:", err)
		return false
	}

	// Encode the request body into snappy encoding.
	compressed := snappy.Encode(nil, raw)
	_, err = client.Store(context.Background(), compressed, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  FAILED:", err)
		return false
	}

	return true
}

type setHeadersTransport struct {
	http.RoundTripper
	headers map[string]string
}

func (s *setHeadersTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for key, value := range s.headers {
		req.Header.Set(key, value)
	}
	return s.RoundTripper.RoundTrip(req)
}

func toTimeseries(wr *prompb.WriteRequest, labels map[string]string, timestamp int64, value float64) {
	var ts prompb.TimeSeries
	ts.Labels = makeLabels(labels)
	ts.Samples = []prompb.Sample{
		{
			Timestamp: timestamp,
			Value:     value,
		},
	}
	wr.Timeseries = append(wr.Timeseries, ts)
}

func makeTimeseries(wr *prompb.WriteRequest, labels map[string]string, m *dto.Metric) error {
	var err error

	timestamp := m.GetTimestampMs()
	if timestamp == 0 {
		timestamp = time.Now().UnixNano() / int64(time.Millisecond)
	}

	switch {
	case m.Gauge != nil:
		toTimeseries(wr, labels, timestamp, m.GetGauge().GetValue())
	case m.Counter != nil:
		toTimeseries(wr, labels, timestamp, m.GetCounter().GetValue())
	case m.Summary != nil:
		metricName := labels[model.MetricNameLabel]
		// Preserve metric name order with first quantile labels timeseries then sum suffix timeseries and finally count suffix timeseries
		// Add Summary quantile timeseries
		quantileLabels := make(map[string]string, len(labels)+1)
		for key, value := range labels {
			quantileLabels[key] = value
		}

		for _, q := range m.GetSummary().Quantile {
			quantileLabels[model.QuantileLabel] = fmt.Sprint(q.GetQuantile())
			toTimeseries(wr, quantileLabels, timestamp, q.GetValue())
		}
		// Overwrite label model.MetricNameLabel for count and sum metrics
		// Add Summary sum timeseries
		labels[model.MetricNameLabel] = metricName + sumStr
		toTimeseries(wr, labels, timestamp, m.GetSummary().GetSampleSum())
		// Add Summary count timeseries
		labels[model.MetricNameLabel] = metricName + countStr
		toTimeseries(wr, labels, timestamp, float64(m.GetSummary().GetSampleCount()))

	case m.Histogram != nil:
		metricName := labels[model.MetricNameLabel]
		// Preserve metric name order with first bucket suffix timeseries then sum suffix timeseries and finally count suffix timeseries
		// Add Histogram bucket timeseries
		bucketLabels := make(map[string]string, len(labels)+1)
		for key, value := range labels {
			bucketLabels[key] = value
		}
		for _, b := range m.GetHistogram().Bucket {
			bucketLabels[model.MetricNameLabel] = metricName + bucketStr
			bucketLabels[model.BucketLabel] = fmt.Sprint(b.GetUpperBound())
			toTimeseries(wr, bucketLabels, timestamp, float64(b.GetCumulativeCount()))
		}
		// Overwrite label model.MetricNameLabel for count and sum metrics
		// Add Histogram sum timeseries
		labels[model.MetricNameLabel] = metricName + sumStr
		toTimeseries(wr, labels, timestamp, m.GetHistogram().GetSampleSum())
		// Add Histogram count timeseries
		labels[model.MetricNameLabel] = metricName + countStr
		toTimeseries(wr, labels, timestamp, float64(m.GetHistogram().GetSampleCount()))

	case m.Untyped != nil:
		toTimeseries(wr, labels, timestamp, m.GetUntyped().GetValue())
	default:
		err = errors.New("unsupported metric type")
	}
	return err
}

func makeLabels(labelsMap map[string]string) []prompb.Label {
	// build labels name list
	sortedLabelNames := make([]string, 0, len(labelsMap))
	for label := range labelsMap {
		sortedLabelNames = append(sortedLabelNames, label)
	}
	// sort labels name in lexicographical order
	sort.Strings(sortedLabelNames)

	var labels []prompb.Label
	for _, label := range sortedLabelNames {
		labels = append(labels, prompb.Label{
			Name:  label,
			Value: labelsMap[label],
		})
	}
	return labels
}

func makeLabelsMap(m *dto.Metric, metricName string, extraLabels map[string]string) map[string]string {
	// build labels map
	labels := make(map[string]string, len(m.Label)+len(extraLabels))
	labels[model.MetricNameLabel] = metricName

	// add extra labels
	for key, value := range extraLabels {
		labels[key] = value
	}

	// add metric labels
	for _, label := range m.Label {
		labelname := label.GetName()
		if labelname == model.JobLabel {
			labelname = fmt.Sprintf("%s%s", model.ExportedLabelPrefix, labelname)
		}

		labelValue := label.GetValue()
		if labelValue == "" || labelname == "" {
			continue
		}

		labels[labelname] = label.GetValue()
	}

	return labels
}
