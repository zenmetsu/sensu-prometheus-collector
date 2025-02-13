package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"math"

	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/client_golang/api/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	exporterAuthID = "exporter"
)

type ExporterAuth struct {
	User     string `envconfig:"user" default:""`
	Password string `envconfig:"password" default:""`
	Header   string `envconfig:"header" default:""`
}

type Tag struct {
	Name  model.LabelName
	Value model.LabelValue
}

type Metric struct {
	Tags  []Tag
	Value float64
}

func CreateJSONMetrics(samples model.Vector) string {
	metrics := []Metric{}

	for _, sample := range samples {
		metric := Metric{}

		for name, value := range sample.Metric {
			tag := Tag{
				Name:  name,
				Value: value,
			}

			metric.Tags = append(metric.Tags, tag)
		}

		metric.Value = float64(sample.Value)

		metrics = append(metrics, metric)
	}

	jsonMetrics, _ := json.Marshal(metrics)

	return string(jsonMetrics)
}

func CreateGraphite2Metrics(samples model.Vector, metricPrefix string) string {
	metrics := ""

	for _, sample := range samples {
		name := fmt.Sprintf("%s%s", metricPrefix, sample.Metric["__name__"])
		name = strings.Replace(name, "{", "_", -1)
		name = strings.Replace(name, "}", "_", -1)
		name = strings.Replace(name, ",", "", -1)

		value := strconv.FormatFloat(float64(0), 'f', -1, 64)
		if math.IsNaN(float64(sample.Value)) == false {
			value = strconv.FormatFloat(float64(sample.Value), 'f', -1, 64)
		}

		now := time.Now()
		timestamp := now.Unix()

		metric := fmt.Sprintf("%s %s %d\n", name, value, timestamp)

		metrics += metric
	}

	return metrics
}

func CreateGraphiteMetrics(samples model.Vector, metricPrefix string) string {
	metrics := ""

	for _, sample := range samples {
		metric := fmt.Sprintf("%s%s", metricPrefix, sample.Metric["__name__"])

		for name, value := range sample.Metric {
			if name != "__name__" {
				tags := fmt.Sprintf(",%s=%s", name, value)
				if !strings.Contains(tags, "\n") && strings.Count(tags, "=") == 1 {
					metric += tags
				}
			}
		}

		metric = strings.Replace(metric, "\n", "", -1)

		value := strconv.FormatFloat(float64(0), 'f', -1, 64)
		if math.IsNaN(float64(sample.Value)) == false {
			value = strconv.FormatFloat(float64(sample.Value), 'f', -1, 64)
		}

		now := time.Now()
		timestamp := now.Unix()

		metric += fmt.Sprintf(" %s %d\n", value, timestamp)

		segments := strings.Split(metric, " ")
		if len(segments) == 3 {
			metrics += metric
		}
	}

	return metrics
}

func CreateInfluxMetrics(samples model.Vector, metricPrefix string) string {
	metrics := ""

	for _, sample := range samples {
		metric := fmt.Sprintf("%s%s", metricPrefix, sample.Metric["__name__"])

		for name, value := range sample.Metric {
			if name != "__name__" {
				tags := fmt.Sprintf(",%s=%s", name, value)
				if !strings.Contains(tags, "\n") && strings.Count(tags, "=") == 1 {
					metric += tags
				}
			}
		}

		metric = strings.Replace(metric, "\n", "", -1)

		value := strconv.FormatFloat(float64(sample.Value), 'f', -1, 64)

		now := time.Now()
		timestamp := now.Unix()

		metric += fmt.Sprintf(" value=%s %d\n", value, timestamp)

		segments := strings.Split(metric, " ")
		if len(segments) == 3 {
			metrics += metric
		}
	}

	return metrics
}

func OutputMetrics(samples model.Vector, outputFormat string, metricPrefix string) error {
	output := ""

	switch outputFormat {
	case "influx":
		output = CreateInfluxMetrics(samples, metricPrefix)
	case "graphite":
		output = CreateGraphiteMetrics(samples, metricPrefix)
	case "json":
		output = CreateJSONMetrics(samples)
	}

	fmt.Print(output)

	return nil
}

func QueryPrometheus(promURL string, queryString string) (model.Vector, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	promConfig := prometheus.Config{Address: promURL}
	promClient, err := prometheus.New(promConfig)

	if err != nil {
		return nil, err
	}

	promQueryClient := prometheus.NewQueryAPI(promClient)

	promResponse, err := promQueryClient.Query(ctx, queryString, time.Now())

	if err != nil {
		return nil, err
	}

	if promResponse.Type() == model.ValVector {
		return promResponse.(model.Vector), nil
	}

	return nil, errors.New("unexpected response type")
}

func QueryExporter(exporterURL string, auth ExporterAuth) (model.Vector, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", exporterURL, nil)

	if err != nil {
		return nil, err
	}

	if auth.User != "" && auth.Password != "" {
		req.SetBasicAuth(auth.User, auth.Password)
	}

	if auth.Header != "" {
		req.Header.Set("Authorization", auth.Header)
	}

	expResponse, err := client.Do(req)

	if err != nil {
		return nil, err
	}
	defer expResponse.Body.Close()

	if expResponse.StatusCode != http.StatusOK {
		return nil, errors.New("exporter returned non OK HTTP response status: " + expResponse.Status)
	}

	var parser expfmt.TextParser

	metricFamilies, err := parser.TextToMetricFamilies(expResponse.Body)

	if err != nil {
		return nil, err
	}

	samples := model.Vector{}

	decodeOptions := &expfmt.DecodeOptions{
		Timestamp: model.Time(time.Now().Unix()),
	}

	for _, family := range metricFamilies {
		familySamples, _ := expfmt.ExtractSamples(decodeOptions, family)
		samples = append(samples, familySamples...)
	}

	return samples, nil
}

func setExporterAuth(user string, password string, header string) (auth ExporterAuth, error error) {
	err := envconfig.Process(exporterAuthID, &auth)

	if err != nil {
		return auth, err
	}

	if user != "" && password != "" {
		auth.User = user
		auth.Password = password
	}

	if header != "" {
		auth.Header = header
	}

	return auth, nil
}

func main() {
	exporterURL := flag.String("exporter-url", "", "Prometheus exporter URL to pull metrics from.")
	exporterUser := flag.String("exporter-user", "", "Prometheus exporter basic auth user.")
	exporterPassword := flag.String("exporter-password", "", "Prometheus exporter basic auth password.")
	exporterAuthorizationHeader := flag.String("exporter-authorization", "", "Prometheus exporter Authorization header.")
	promURL := flag.String("prom-url", "http://localhost:9090", "Prometheus API URL.")
	queryString := flag.String("prom-query", "up", "Prometheus API query string.")
	outputFormat := flag.String("output-format", "influx", "The check output format to use for metrics {influx|graphite|json}.")
	metricPrefix := flag.String("metric-prefix", "", "Metric name prefix, only supported by line protocol output formats.")
	flag.Parse()

	var samples model.Vector
	var err error

	if *exporterURL != "" {
		auth, err := setExporterAuth(*exporterUser, *exporterPassword, *exporterAuthorizationHeader)

		if err != nil {
			_ = fmt.Errorf("%v", err)
			os.Exit(2)
		}

		samples, err = QueryExporter(*exporterURL, auth)

		if err != nil {
			_ = fmt.Errorf("%v", err)
			os.Exit(2)
		}

	} else {
		samples, err = QueryPrometheus(*promURL, *queryString)

		if err != nil {
			_ = fmt.Errorf("%v", err)
			os.Exit(2)
		}
	}

	err = OutputMetrics(samples, *outputFormat, *metricPrefix)

	if err != nil {
		_ = fmt.Errorf("%v", err)
		os.Exit(2)
	}
}
