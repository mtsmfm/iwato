package main

// testConfig is a configuration with one "dev" sandbox for tests.
func testConfig() Config {
	c := defaultConfig()
	c.Sandboxes = []Sandbox{newSandbox("dev")}
	return c
}
