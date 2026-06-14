// Package carbonrightsize implements the CarbonRightSizing recommender plugin.
// It right-sizes CPU and memory resources for pods that are energy-inefficient,
// using Kepler energy metrics from Prometheus to compute an energy-efficiency
// ratio and recommending requests/limits based on historical usage percentiles.
package carbonrightsize
