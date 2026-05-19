package datagen

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
)

// Config configures data generation.
type Config struct {
	// Cardinality settings
	NumSeries        int      // Total number of unique series to generate
	MetricName       string   // Base metric name
	LabelNames       []string // Label names to use
	LabelCardinality []int    // Cardinality per label (must match LabelNames length)

	// Info metrics for joins
	InfoMetrics []InfoMetricConfig

	// Time settings
	SampleInterval time.Duration // Interval between samples
	Duration       time.Duration // Total duration to generate

	// Churn settings (for high-cardinality testing)
	ChurnRate     float64       // Fraction of churnable series replaced per ChurnInterval (0-1)
	ChurnInterval time.Duration // How often churn occurs (default: 5m)
	ChurnFraction float64       // Fraction of all series that can churn (0-1), rest are stable
}

// InfoMetricConfig configures an info metric for join testing.
type InfoMetricConfig struct {
	Name       string            // Metric name (e.g., "kube_pod_info")
	JoinLabel  string            // Label to join on (e.g., "pod")
	InfoLabels map[string]string // Additional info labels to add
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		NumSeries:        1000,
		MetricName:       "test_metric",
		LabelNames:       []string{"instance", "job", "env"},
		LabelCardinality: []int{100, 10, 3},
		SampleInterval:   15 * time.Second,
		Duration:         1 * time.Hour,
	}
}

// Generator generates test metrics.
type Generator struct {
	config Config
	rng    *rand.Rand
}

// NewGenerator creates a new data generator.
func NewGenerator(cfg Config) *Generator {
	return &Generator{
		config: cfg,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GenerateRemoteWrite generates samples and sends them via remote write.
func (g *Generator) GenerateRemoteWrite(ctx context.Context, prometheusURL string) error {
	// Generate all series label combinations
	seriesLabels := g.generateSeriesLabels()

	// Calculate time range
	endTime := time.Now()
	startTime := endTime.Add(-g.config.Duration)

	// Generate samples in batches
	batchSize := 500
	currentTime := startTime
	totalSamples := 0

	for currentTime.Before(endTime) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Build batch of time series
		var timeSeries []prompb.TimeSeries
		for _, labels := range seriesLabels {
			value := g.generateValue(labels)
			ts := prompb.TimeSeries{
				Labels:  labels,
				Samples: []prompb.Sample{{Value: value, Timestamp: currentTime.UnixMilli()}},
			}
			timeSeries = append(timeSeries, ts)

			if len(timeSeries) >= batchSize {
				if err := g.sendRemoteWrite(ctx, prometheusURL, timeSeries); err != nil {
					return fmt.Errorf("send batch: %w", err)
				}
				totalSamples += len(timeSeries)
				timeSeries = timeSeries[:0]
			}
		}

		// Send remaining samples
		if len(timeSeries) > 0 {
			if err := g.sendRemoteWrite(ctx, prometheusURL, timeSeries); err != nil {
				return fmt.Errorf("send batch: %w", err)
			}
			totalSamples += len(timeSeries)
		}

		currentTime = currentTime.Add(g.config.SampleInterval)
	}

	fmt.Printf("Sent %d samples\n", totalSamples)
	return nil
}

// generateSeriesLabels generates all label combinations as prompb.Label slices.
func (g *Generator) generateSeriesLabels() [][]prompb.Label {
	if len(g.config.LabelNames) == 0 {
		return [][]prompb.Label{{
			{Name: "__name__", Value: g.config.MetricName},
		}}
	}

	// Generate label values for each label
	labelValues := make([][]string, len(g.config.LabelNames))
	for i, cardinality := range g.config.LabelCardinality {
		labelValues[i] = make([]string, cardinality)
		for j := 0; j < cardinality; j++ {
			labelValues[i][j] = fmt.Sprintf("%s_%d", g.config.LabelNames[i], j)
		}
	}

	// Generate combinations up to NumSeries
	var combinations [][]prompb.Label
	g.generateLabelCombinations(labelValues, 0, []prompb.Label{
		{Name: "__name__", Value: g.config.MetricName},
	}, &combinations)

	// Limit to NumSeries
	if len(combinations) > g.config.NumSeries {
		combinations = combinations[:g.config.NumSeries]
	}

	return combinations
}

func (g *Generator) generateLabelCombinations(labelValues [][]string, depth int, current []prompb.Label, result *[][]prompb.Label) {
	if len(*result) >= g.config.NumSeries {
		return
	}

	if depth >= len(labelValues) {
		combo := make([]prompb.Label, len(current))
		copy(combo, current)
		*result = append(*result, combo)
		return
	}

	for _, value := range labelValues[depth] {
		next := append(current, prompb.Label{
			Name:  g.config.LabelNames[depth],
			Value: value,
		})
		g.generateLabelCombinations(labelValues, depth+1, next, result)
	}
}

func (g *Generator) generateValue(labels []prompb.Label) float64 {
	// Generate a value based on labels with some randomness
	hash := 0
	for _, l := range labels {
		for _, c := range l.Value {
			hash = hash*31 + int(c)
		}
	}
	base := float64(hash%1000) / 10.0
	return base + g.rng.Float64()*10
}

func (g *Generator) sendRemoteWrite(ctx context.Context, prometheusURL string, timeSeries []prompb.TimeSeries) error {
	writeReq := &prompb.WriteRequest{
		Timeseries: timeSeries,
	}

	data, err := proto.Marshal(writeReq)
	if err != nil {
		return fmt.Errorf("marshal write request: %w", err)
	}

	compressed := snappy.Encode(nil, data)

	req, err := http.NewRequestWithContext(ctx, "POST", prometheusURL+"/api/v1/write", bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("remote write failed: %s", resp.Status)
	}
	return nil
}

// SeriesCount returns the actual number of series that will be generated.
func (g *Generator) SeriesCount() int {
	total := 1
	for _, c := range g.config.LabelCardinality {
		total *= c
	}
	if total > g.config.NumSeries {
		return g.config.NumSeries
	}
	return total
}

// labelsToString converts labels to a string for debugging.
func labelsToString(labels []prompb.Label) string {
	var parts []string
	for _, l := range labels {
		if l.Name != "__name__" {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, l.Name, l.Value))
		}
	}
	return strings.Join(parts, ",")
}

// GenerateOpenMetrics generates data in OpenMetrics format for promtool backfill.
// Returns the generated text and the time range covered.
func (g *Generator) GenerateOpenMetrics(startTime, endTime time.Time) (string, error) {
	var buf bytes.Buffer

	// Write TYPE and HELP for each metric
	buf.WriteString(fmt.Sprintf("# HELP %s Test metric for backfill\n", g.config.MetricName))
	buf.WriteString(fmt.Sprintf("# TYPE %s gauge\n", g.config.MetricName))

	// Handle churn if configured
	if g.config.ChurnRate > 0 && g.config.ChurnInterval > 0 {
		g.generateOpenMetricsWithChurn(&buf, startTime, endTime)
	} else {
		g.generateOpenMetricsStatic(&buf, startTime, endTime)
	}

	// OpenMetrics requires EOF marker
	buf.WriteString("# EOF\n")

	return buf.String(), nil
}

// generateOpenMetricsStatic generates samples without churn (all series live for entire duration)
func (g *Generator) generateOpenMetricsStatic(buf *bytes.Buffer, startTime, endTime time.Time) {
	seriesLabels := g.generateSeriesLabels()

	currentTime := startTime
	for currentTime.Before(endTime) || currentTime.Equal(endTime) {
		for _, labels := range seriesLabels {
			g.writeOpenMetricsSample(buf, labels, currentTime)
		}
		currentTime = currentTime.Add(g.config.SampleInterval)
	}
}

// generateOpenMetricsWithChurn generates samples with series churn (labels change over time)
func (g *Generator) generateOpenMetricsWithChurn(buf *bytes.Buffer, startTime, endTime time.Time) {
	// Track active series with their lifecycle
	type seriesLife struct {
		labels    []prompb.Label
		startTime time.Time
		endTime   time.Time
		churnable bool // Whether this series can be churned
	}

	// Generate initial series pool
	baseLabels := g.generateSeriesLabels()
	churnCounter := 0

	// Determine how many series can churn (default to all if ChurnFraction not set)
	churnFraction := g.config.ChurnFraction
	if churnFraction <= 0 {
		churnFraction = 1.0 // All series can churn by default
	}
	numChurnable := int(float64(len(baseLabels)) * churnFraction)
	if numChurnable < 1 && churnFraction > 0 {
		numChurnable = 1
	}

	// Create initial active series - first numChurnable are churnable, rest are stable
	activeSeries := make([]seriesLife, len(baseLabels))
	for i, labels := range baseLabels {
		activeSeries[i] = seriesLife{
			labels:    labels,
			startTime: startTime,
			endTime:   endTime,
			churnable: i < numChurnable,
		}
	}

	// Calculate churn parameters - only apply to churnable series
	numToChurn := int(float64(numChurnable) * g.config.ChurnRate)
	if numToChurn < 1 && g.config.ChurnRate > 0 {
		numToChurn = 1
	}

	currentTime := startTime
	lastChurnTime := startTime

	for currentTime.Before(endTime) || currentTime.Equal(endTime) {
		// Check if it's time to churn
		if currentTime.Sub(lastChurnTime) >= g.config.ChurnInterval && currentTime.Before(endTime) {
			// Build list of churnable series indices
			var churnableIndices []int
			for i, s := range activeSeries {
				if s.churnable && (currentTime.Before(s.endTime) || currentTime.Equal(s.endTime)) {
					churnableIndices = append(churnableIndices, i)
				}
			}

			// Churn: end some churnable series and create new ones
			if len(churnableIndices) > 0 {
				toChurn := numToChurn
				if toChurn > len(churnableIndices) {
					toChurn = len(churnableIndices)
				}
				perm := g.rng.Perm(len(churnableIndices))[:toChurn]
				for _, permIdx := range perm {
					idx := churnableIndices[permIdx]
					// End the old series
					activeSeries[idx].endTime = currentTime

					// Create a new series with modified label
					newLabels := make([]prompb.Label, len(activeSeries[idx].labels))
					copy(newLabels, activeSeries[idx].labels)

					// Modify the first non-__name__ label to create a new series
					for i, l := range newLabels {
						if l.Name != "__name__" {
							churnCounter++
							newLabels[i] = prompb.Label{
								Name:  l.Name,
								Value: fmt.Sprintf("%s_churn_%d", l.Value, churnCounter),
							}
							break
						}
					}

					// Add the new series (also churnable)
					activeSeries = append(activeSeries, seriesLife{
						labels:    newLabels,
						startTime: currentTime,
						endTime:   endTime,
						churnable: true,
					})
				}
			}
			lastChurnTime = currentTime
		}

		// Write samples for all active series at this timestamp
		for _, series := range activeSeries {
			if (currentTime.Equal(series.startTime) || currentTime.After(series.startTime)) &&
				(currentTime.Equal(series.endTime) || currentTime.Before(series.endTime)) {
				g.writeOpenMetricsSample(buf, series.labels, currentTime)
			}
		}

		currentTime = currentTime.Add(g.config.SampleInterval)
	}
}

// writeOpenMetricsSample writes a single sample in OpenMetrics format
func (g *Generator) writeOpenMetricsSample(buf *bytes.Buffer, labels []prompb.Label, timestamp time.Time) {
	value := g.generateValue(labels)

	var metricName string
	var labelParts []string
	for _, l := range labels {
		if l.Name == "__name__" {
			metricName = l.Value
		} else {
			labelParts = append(labelParts, fmt.Sprintf(`%s="%s"`, l.Name, l.Value))
		}
	}

	if len(labelParts) > 0 {
		buf.WriteString(fmt.Sprintf("%s{%s} %g %.3f\n",
			metricName,
			strings.Join(labelParts, ","),
			value,
			float64(timestamp.UnixMilli())/1000.0))
	} else {
		buf.WriteString(fmt.Sprintf("%s %g %.3f\n",
			metricName,
			value,
			float64(timestamp.UnixMilli())/1000.0))
	}
}
