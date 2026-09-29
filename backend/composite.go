package backend

// CompositeBackend routes paths to backends by longest matching prefix and
// merges ls/grep/glob results across routes.
type CompositeBackend struct {
	unimplemented
	defaultBackend Backend
	// routes maps prefixes such as "/memories/" to backends.
	routes map[string]Backend
	// artifactsRoot is where offloaded artifacts (messages, outputs) are written.
	artifactsRoot string
}

// NewCompositeBackend returns a router over routes, falling back to def.
func NewCompositeBackend(def Backend, routes map[string]Backend, artifactsRoot string) *CompositeBackend {
	if artifactsRoot == "" {
		artifactsRoot = "/"
	}
	return &CompositeBackend{
		unimplemented:  unimplemented{name: "CompositeBackend"},
		defaultBackend: def,
		routes:         routes,
		artifactsRoot:  artifactsRoot,
	}
}

// ID identifies the composite; execute is delegated to the default backend.
func (b *CompositeBackend) ID() string { return "composite" }
