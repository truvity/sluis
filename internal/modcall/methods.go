package modcall

import "slices"

// Method describes one registered method, for a lint over what a module
// registered: it carries no handler.
type Method struct {
	Name string
	// Allow is the caller classes the method answers, sorted. Nil is any
	// caller the transport let in: no [Allow] was registered.
	Allow []string
	// MaxBytes is the bound of the request payload and of the result.
	MaxBytes int
}

// Methods lists the methods registered on s, sorted by name.
func (s *Server) Methods() []Method {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Method, 0, len(s.handlers))
	for name, h := range s.handlers {
		m := Method{Name: name, MaxBytes: h.max}
		if h.allow != nil {
			m.Allow = make([]string, 0, len(h.allow))
			for c := range h.allow {
				m.Allow = append(m.Allow, c)
			}
			slices.Sort(m.Allow)
			if len(m.Allow) == 0 {
				m.Allow = []string{}
			}
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Method) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}
