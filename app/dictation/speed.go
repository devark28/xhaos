package dictation

import (
	"encoding/json"
	"math"
	"sync"
)

// The server's transcription speed, measured on its own recent takes, so the
// page's "about N s" fits this machine (a laptop and a small VPS differ
// several times over) and survives a reload.
//
// A take costs a fixed part (starting the sidecar, loading the model) plus a
// part that grows with its length. With takes of different lengths both are
// fitted; until then the estimate is a plain ratio. A take answered by the warm
// service has no load in it, so its samples are kept apart from the others'
// (the command's): the estimate uses the kind the server is set up for.

const (
	speedKey     = "speed" // in dictation_settings, server scope
	speedSamples = 8       // recent takes kept
	minSpread    = 10.0    // seconds between the shortest and longest take before fitting a fixed part
)

// sample is one take: its length and how long the sidecar took, in seconds.
// Cut marks a take that timed out: Took is then its limit, a lower bound.
// Warm marks a take answered by the warm service (see warm.go).
type sample struct {
	Length float64 `json:"length"`
	Took   float64 `json:"took"`
	Cut    bool    `json:"cut,omitempty"`
	Warm   bool    `json:"warm,omitempty"`
}

var speedMu sync.Mutex // one read-modify-write of the samples at a time

// recordSpeed adds a take to the recent samples.
func (s *Service) recordSpeed(length, took float64, cut, warm bool) error {
	if length < 1 {
		return nil
	}
	speedMu.Lock()
	defer speedMu.Unlock()
	samples, err := s.samples()
	if err != nil {
		return err
	}
	samples = append(samples, sample{Length: length, Took: took, Cut: cut, Warm: warm})
	if len(samples) > speedSamples {
		samples = samples[len(samples)-speedSamples:]
	}
	data, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	return s.putSetting("", speedKey, string(data))
}

func (s *Service) samples() ([]sample, error) {
	v, err := s.setting("", speedKey)
	if err != nil || v == "" {
		return nil, err
	}
	var out []sample
	if json.Unmarshal([]byte(v), &out) != nil {
		return nil, nil // unreadable: start over
	}
	return out, nil
}

// Estimate returns how many seconds this server should take to transcribe a
// take of the given length, from its recent takes; 0 when it has none yet.
func (s *Service) Estimate(seconds float64) (float64, error) {
	samples, err := s.samples()
	if err != nil {
		return 0, err
	}
	return estimate(ofKind(samples, s.cfg.URL != ""), seconds), nil
}

// ofKind returns the samples of the kind this server takes (warm when a
// service is set). Until it has any of that kind, all of them.
func ofKind(samples []sample, warm bool) []sample {
	var out []sample
	for _, p := range samples {
		if p.Warm == warm {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return samples
	}
	return out
}

func estimate(samples []sample, length float64) float64 {
	if len(samples) == 0 || length <= 0 {
		return 0
	}
	var n, sx, sy, sxx, sxy float64
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range samples {
		n++
		sx, sy, sxx, sxy = sx+p.Length, sy+p.Took, sxx+p.Length*p.Length, sxy+p.Length*p.Took
		lo, hi = min(lo, p.Length), max(hi, p.Length)
	}
	// Least squares: took = fixed + rate * length, both kept plausible.
	if hi-lo >= minSpread {
		if d := n*sxx - sx*sx; d > 0 {
			rate := (n*sxy - sx*sy) / d
			fixed := (sy - rate*sx) / n
			if rate > 0 {
				return math.Round(max(1, min(fixed, 60)+rate*length))
			}
		}
	}
	return math.Round(max(1, sy/sx*length))
}
