// Package router resolves an incoming Claude model name to a backend provider
// and upstream model id, using the route table from config.
package router

import (
	"fmt"
	"strings"

	"github.com/relaycode/relaycode/internal/config"
)

type Resolved struct {
	ProviderName string
	Provider     config.ProviderConfig
	Model        string
}

type Router struct {
	routes    []config.Route
	providers map[string]config.ProviderConfig
}

func New(cfg *config.Config) *Router {
	return &Router{routes: cfg.Routes, providers: cfg.Providers}
}

// Resolve picks the first route whose Match is a substring of model
// (case-insensitive). "*" is the fallback and matches anything.
func (r *Router) Resolve(model string) (Resolved, error) {
	name := strings.ToLower(model)
	var fallback *config.Route
	for i := range r.routes {
		rt := &r.routes[i]
		if rt.Match == "*" {
			fallback = rt
			continue
		}
		if strings.Contains(name, strings.ToLower(rt.Match)) {
			return r.bind(rt)
		}
	}
	if fallback != nil {
		return r.bind(fallback)
	}
	return Resolved{}, fmt.Errorf("no route matches model %q and no fallback configured", model)
}

func (r *Router) bind(rt *config.Route) (Resolved, error) {
	p, ok := r.providers[rt.Provider]
	if !ok {
		return Resolved{}, fmt.Errorf("route %q references unknown provider %q", rt.Match, rt.Provider)
	}
	return Resolved{
		ProviderName: rt.Provider,
		Provider:     p,
		Model:        rt.Model,
	}, nil
}
