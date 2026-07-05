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

// ElectricityMaps API base URL and default zone.
const (
	electricityMapsBaseURL = "https://api.electricitymaps.com/v3"
	defaultZone            = "SE" // Sweden
)

// CarbonIntensityEntry represents a single data point from the Electricity Maps API.
type CarbonIntensityEntry struct {
	Zone            string  `json:"zone"`
	CarbonIntensity float64 `json:"carbonIntensity"`
	Datetime        string  `json:"datetime"`
	UpdatedAt       string  `json:"updatedAt"`
}

// CarbonIntensityHistory represents the API response for carbon intensity history.
type CarbonIntensityHistory struct {
	Zone    string                 `json:"zone"`
	History []CarbonIntensityEntry `json:"history"`
}

// HourlyCarbonIntensity holds the average carbon intensity per hour (0-23).
type HourlyCarbonIntensity struct {
	Zone       string
	HourlyGCO2 [24]float64 // gCO2/kWh per hour of day (UTC)
	FetchedAt  time.Time
}

// carbonIntensityCache caches the result to avoid repeated API calls within the same cycle.
var (
	ciCache     *HourlyCarbonIntensity
	ciCacheMu   sync.Mutex
	ciCacheTTL  = 30 * time.Minute
)

// GetHourlyCarbonIntensity fetches the 24h carbon intensity history from Electricity Maps
// and returns the average gCO2/kWh per hour of day. Results are cached for ciCacheTTL.
// If the API key is not set or the API fails, returns nil (caller should fall back to config).
func GetHourlyCarbonIntensity() *HourlyCarbonIntensity {
	ciCacheMu.Lock()
	defer ciCacheMu.Unlock()

	// Return cache if fresh.
	if ciCache != nil && time.Since(ciCache.FetchedAt) < ciCacheTTL {
		return ciCache
	}

	apiKey := os.Getenv("ELECTRICITY_MAPS_API_KEY")
	if apiKey == "" {
		klog.V(2).Infof("CarbonLoadShifting: ELECTRICITY_MAPS_API_KEY not set, using static config")
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
		klog.Warningf("CarbonLoadShifting: failed to create request: %v", err)
		return nil
	}
	req.Header.Set("auth-token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		klog.Warningf("CarbonLoadShifting: Electricity Maps API call failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		klog.Warningf("CarbonLoadShifting: Electricity Maps API returned %d: %s", resp.StatusCode, string(body))
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		klog.Warningf("CarbonLoadShifting: failed to read response body: %v", err)
		return nil
	}

	var history CarbonIntensityHistory
	if err := json.Unmarshal(body, &history); err != nil {
		// Try parsing as array directly (some API versions return array of entries).
		var entries []CarbonIntensityEntry
		if err2 := json.Unmarshal(body, &entries); err2 != nil {
			klog.Warningf("CarbonLoadShifting: failed to parse API response: %v (also tried array: %v)", err, err2)
			return nil
		}
		history.Zone = zone
		history.History = entries
	}

	if len(history.History) == 0 {
		klog.Warningf("CarbonLoadShifting: Electricity Maps returned empty history for zone %s", zone)
		return nil
	}

	// Bucket by hour of day (UTC).
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

	klog.Infof("CarbonLoadShifting: fetched real carbon intensity for zone %s (%d data points)", zone, len(history.History))
	ciCache = result
	return result
}

// FindOptimalWindow finds the lowest-carbon window of the given duration (hours).
// Returns startHour and the average gCO2/kWh during that window.
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

// FindPeakWindow finds the highest-carbon window of the given duration (hours).
// Returns startHour and the average gCO2/kWh during that window.
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
