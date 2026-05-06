// Package cost tracks token usage and dollar cost per session, aggregates
// across the swarm, and persists historical totals.
package cost

import "github.com/calebcorpening/swarm/internal/agent"

type Rate struct {
	Model         string
	InputPerMTok  float64
	OutputPerMTok float64
}

// DefaultRates is a starting point shipped with the binary; users override in config.
var DefaultRates = []Rate{
	{Model: "claude-opus-4-7", InputPerMTok: 15.00, OutputPerMTok: 75.00},
	{Model: "claude-sonnet-4-6", InputPerMTok: 3.00, OutputPerMTok: 15.00},
	{Model: "claude-haiku-4-5", InputPerMTok: 1.00, OutputPerMTok: 5.00},
}

type Aggregate struct {
	Input    int
	Output   int
	CacheHit int
	USD      float64
}

func (a *Aggregate) Add(u agent.TokenUsage, rates []Rate) {
	a.Input += u.Input
	a.Output += u.Output
	a.CacheHit += u.CacheHit
	for _, r := range rates {
		if r.Model == u.Model {
			a.USD += float64(u.Input)/1_000_000*r.InputPerMTok +
				float64(u.Output)/1_000_000*r.OutputPerMTok
			return
		}
	}
}
