//go:build !linux

package hostmetrics

func newCollector(_ bool) *Collector { return &Collector{} }
