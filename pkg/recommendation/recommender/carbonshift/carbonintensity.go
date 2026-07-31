package carbonshift

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	electricityMapsBaseURL = "https://api.electricitymaps.com/v3"
	defaultZone            = "SE"
)

type CarbonIntensityEntry struct {
	Zone            string  `json:"zone"`
	CarbonIntensity float64 `json:"carbonIntensity"`
	Datetime        string  `json:"datetime"`
	UpdatedAt       string  `json:"updatedAt"`
}

type CarbonIntensityHistory struct {
	Zone    string                 `json:"zone"`
	History []CarbonIntensityEntry `json:"history"`
}

type HourlyCarbonIntensity struct {
	Zone       string
	HourlyGCO2 [24]float64
	FetchedAt  time.Time
}

var (
	ciCache     *HourlyCarbonIntensity
	ciCacheMu   sync.Mutex
	ciCacheTTL  = 30 * time.Minute
)

func GetHourlyCarbonIntensity() *HourlyCarbonIntensity {
	ciCacheMu.Lock()
	defer ciCacheMu.Unlock()

	if ciCache != nil && time.Since(ciCache.FetchedAt) < ciCacheTTL {
		return ciCache
	}

	apiKey := os.Getenv("ELECTRICITY_MAPS_API_KEY")
	if apiKey == "" {
		klog.V(2).Infof("CarbonTemporalShifting: ELECTRICITY_MAPS_API_KEY not set, using static config")
		return nil
	}

	zone := os.Getenv("ELECTRICITY_MAPS_ZONE")
	if zone == "" {
		zone = defaultZone
	}

	url := fmt.Sprintf("%s/carbon-intensity/history?zone=%s", electricityMapsBaseURL, zone)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		klog.Warningf("CarbonTemporalShifting: failed to create request: %v", err)
		return nil
	}
	req.Header.Set("auth-token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		klog.Warningf("CarbonTemporalShifting: Electricity Maps API call failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		klog.Warningf("CarbonTemporalShifting: Electricity Maps API returned %d: %s", resp.StatusCode, string(body))
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		klog.Warningf("CarbonTemporalShifting: failed to read response body: %v", err)
		return nil
	}

	var history CarbonIntensityHistory
	if err := json.Unmarshal(body, &history); err != nil {
		var entries []CarbonIntensityEntry
		if err2 := json.Unmarshal(body, &entries); err2 != nil {
			klog.Warningf("CarbonTemporalShifting: failed to parse API response: %v (also tried array: %v)", err, err2)
			return nil
		}
		history.Zone = zone
		history.History = entries
	}

	if len(history.History) == 0 {
		klog.Warningf("CarbonTemporalShifting: Electricity Maps returned empty history for zone %s", zone)
		return nil
	}

	var hourSums [24]float64
	var hourCounts [24]int

	for _, entry := range history.History {
		t, err := time.Parse(time.RFC3339, entry.Datetime)
		if err != nil {
			continue
		}
		hour := t.UTC().Hour()
		hourSums[hour] += entry.CarbonIntensity
		hourCounts[hour]++
	}

	result := &HourlyCarbonIntensity{
		Zone:      zone,
		FetchedAt: time.Now(),
	}
	for h := 0; h < 24; h++ {
		if hourCounts[h] > 0 {
			result.HourlyGCO2[h] = hourSums[h] / float64(hourCounts[h])
		}
	}

	klog.Infof("CarbonTemporalShifting: fetched real carbon intensity for zone %s (%d data points)", zone, len(history.History))
	ciCache = result
	return result
}

func (ci *HourlyCarbonIntensity) FindOptimalWindow(windowHours int) (startHour int, avgIntensity float64) {
	if windowHours <= 0 || windowHours > 24 {
		windowHours = 6
	}

	bestStart := 0
	bestAvg := float64(999999)

	for start := 0; start < 24; start++ {
		var sum float64
		for i := 0; i < windowHours; i++ {
			hour := (start + i) % 24
			sum += ci.HourlyGCO2[hour]
		}
		avg := sum / float64(windowHours)
		if avg < bestAvg {
			bestAvg = avg
			bestStart = start
		}
	}

	return bestStart, bestAvg
}

func (ci *HourlyCarbonIntensity) FindPeakWindow(windowHours int) (startHour int, avgIntensity float64) {
	if windowHours <= 0 || windowHours > 24 {
		windowHours = 6
	}

	worstStart := 0
	worstAvg := float64(0)

	for start := 0; start < 24; start++ {
		var sum float64
		for i := 0; i < windowHours; i++ {
			hour := (start + i) % 24
			sum += ci.HourlyGCO2[hour]
		}
		avg := sum / float64(windowHours)
		if avg > worstAvg {
			worstAvg = avg
			worstStart = start
		}
	}

	return worstStart, worstAvg
}

func GetMultiZoneCarbonIntensity(zones []string) map[string]float64 {
	apiKey := os.Getenv("ELECTRICITY_MAPS_API_KEY")
	if apiKey == "" {
		klog.V(2).Infof("CarbonSpatialShifting: ELECTRICITY_MAPS_API_KEY not set")
		return nil
	}

	result := make(map[string]float64)
	client := &http.Client{Timeout: 10 * time.Second}

	for _, zone := range zones {
		url := fmt.Sprintf("%s/carbon-intensity/latest?zone=%s", electricityMapsBaseURL, zone)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			klog.Warningf("CarbonSpatialShifting: failed to create request for zone %s: %v", zone, err)
			continue
		}
		req.Header.Set("auth-token", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			klog.Warningf("CarbonSpatialShifting: API call failed for zone %s: %v", zone, err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			klog.Warningf("CarbonSpatialShifting: API returned %d for zone %s", resp.StatusCode, zone)
			continue
		}

		var entry CarbonIntensityEntry
		if err := json.Unmarshal(body, &entry); err != nil {
			klog.Warningf("CarbonSpatialShifting: failed to parse response for zone %s: %v", zone, err)
			continue
		}

		if entry.CarbonIntensity > 0 {
			result[zone] = entry.CarbonIntensity
		}
	}

	if len(result) == 0 {
		return nil
	}

	klog.Infof("CarbonSpatialShifting: fetched carbon intensity for %d zones", len(result))
	return result
}
