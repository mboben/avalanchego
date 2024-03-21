// Copyright (C) 2019-2024, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package bootstrap

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ava-labs/avalanchego/utils"
)

type metrics struct {
	numFetched, numAccepted prometheus.Counter
	fetchETA                prometheus.Gauge
}

func newMetrics(namespace string, registerer prometheus.Registerer) (*metrics, error) {
	m := &metrics{
		numFetched: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "fetched",
			Help:      "Number of blocks fetched during bootstrapping",
		}),
		numAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "accepted",
			Help:      "Number of blocks accepted during bootstrapping",
		}),
		fetchETA: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "eta_fetching_complete",
			Help:      "ETA in nanoseconds until fetching phase of bootstrapping finishes",
		}),
	}

	err := utils.Err(
		registerer.Register(m.numFetched),
		registerer.Register(m.numAccepted),
		registerer.Register(m.fetchETA),
	)
	return m, err
}
