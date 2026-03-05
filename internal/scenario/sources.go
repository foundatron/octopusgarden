package scenario

// CaptureSourceMap returns the valid capture sources for each step type,
// derived from each executor's ValidCaptureSources method.
// Executors are constructed at zero value; ValidCaptureSources must be
// stateless (no dependency on initialized fields).
func CaptureSourceMap() map[string]map[string]bool {
	types := map[string]StepExecutor{
		"exec":    &ExecExecutor{},
		"browser": &BrowserExecutor{},
		"grpc":    &GRPCExecutor{},
		"request": &HTTPExecutor{},
	}

	result := make(map[string]map[string]bool, len(types))
	for name, exec := range types {
		sources := exec.ValidCaptureSources()
		if len(sources) == 0 {
			continue
		}
		m := make(map[string]bool, len(sources))
		for _, s := range sources {
			m[s] = true
		}
		result[name] = m
	}
	return result
}
