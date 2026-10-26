package socket

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/limiter"
	"github.com/go-gost/x/registry"
)

var limiterConfigMu sync.Mutex

func createLimiter(req createLimiterRequest) error {
	limiterConfigMu.Lock()
	defer limiterConfigMu.Unlock()
	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		return errors.New("limiter name is required")
	}
	req.Data.Name = name

	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		return errors.New("limiter " + name + " already exists")
	}

	v := parser.ParseTrafficLimiter(&req.Data)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		if closer, ok := v.(io.Closer); ok {
			closer.Close()
		}
		return errors.New("limiter " + name + " already exists")
	}

	config.OnUpdate(func(c *config.Config) error {
		c.Limiters = append(c.Limiters, &req.Data)
		return nil
	})

	return nil
}

func updateLimiter(req updateLimiterRequest) error {
	limiterConfigMu.Lock()
	defer limiterConfigMu.Unlock()

	name := strings.TrimSpace(req.Limiter)

	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		return errors.New("limiter " + name + " not found")
	}

	req.Data.Name = name

	var oldConfig *config.LimiterConfig
	config.OnUpdate(func(c *config.Config) error {
		for _, item := range c.Limiters {
			if item != nil && item.Name == name {
				copy := *item
				oldConfig = &copy
				break
			}
		}
		return nil
	})
	updated := false
	if sameLimiterPolicy(oldConfig, &req.Data) {
		if updater, ok := registry.TrafficLimiterRegistry().Get(name).(interface {
			TryUpdateLimits([]string) (bool, error)
		}); ok {
			var err error
			updated, err = updater.TryUpdateLimits(req.Data.Limits)
			if err != nil {
				return err
			}
		}
	}
	if !updated {
		v := parser.ParseTrafficLimiter(&req.Data)
		if err := registry.ReplaceTrafficLimiter(name, v); err != nil {
			if closer, ok := v.(io.Closer); ok {
				closer.Close()
			}
			return err
		}
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Limiters {
			if c.Limiters[i].Name == name {
				c.Limiters[i] = &req.Data
				break
			}
		}
		return nil
	})

	return nil
}

func deleteLimiter(req deleteLimiterRequest) error {
	limiterConfigMu.Lock()
	defer limiterConfigMu.Unlock()

	name := strings.TrimSpace(req.Limiter)

	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		return errors.New("limiter " + name + " not found")
	}
	registry.TrafficLimiterRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		limiteres := c.Limiters
		c.Limiters = nil
		for _, s := range limiteres {
			if s.Name == name {
				continue
			}
			c.Limiters = append(c.Limiters, s)
		}
		return nil
	})

	return nil
}

func sameLimiterPolicy(old, next *config.LimiterConfig) bool {
	if old == nil || next == nil || old.Plugin != nil || next.Plugin != nil {
		return false
	}
	a, b := *old, *next
	a.Name, b.Name = "", ""
	a.Limits, b.Limits = nil, nil
	return reflect.DeepEqual(a, b)
}

type createLimiterRequest struct {
	Data config.LimiterConfig `json:"data"`
}

type updateLimiterRequest struct {
	Limiter string               `json:"limiter"`
	Data    config.LimiterConfig `json:"data"`
}

type deleteLimiterRequest struct {
	Limiter string `json:"limiter"`
}
