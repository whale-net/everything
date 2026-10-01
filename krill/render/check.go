package render

// Stale is implemented in the Implementation lane.
func Stale(files Files, read func(rel string) (string, bool)) []string { return nil }
