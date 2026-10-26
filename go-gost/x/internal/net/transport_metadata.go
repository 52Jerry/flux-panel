package net

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/go-gost/core/metadata"
)

// TransportTimeout accepts a duration string or numeric seconds from both YAML
// and JSON. An explicit zero disables the timeout; invalid values must not
// silently turn off protection.
func TransportTimeout(md metadata.Metadata, key string, fallback time.Duration) (time.Duration, error) {
	if md == nil || !md.IsExists(key) {
		return fallback, nil
	}
	v := md.Get(key)
	var seconds float64
	switch value := v.(type) {
	case time.Duration:
		if value < 0 {
			return 0, fmt.Errorf("%s must not be negative", key)
		}
		return value, nil
	case string:
		if d, err := time.ParseDuration(value); err == nil {
			if d < 0 {
				return 0, fmt.Errorf("%s must not be negative", key)
			}
			return d, nil
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %q", key, value)
		}
		seconds = n
	case int:
		seconds = float64(value)
	case int64:
		seconds = float64(value)
	case float64:
		seconds = value
	case json.Number:
		n, err := value.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %v", key, err)
		}
		seconds = n
	default:
		return 0, fmt.Errorf("invalid %s type %T", key, v)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("%s seconds out of range", key)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
