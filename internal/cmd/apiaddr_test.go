package cmd

import "testing"

func TestLocalAPIAddr_FollowsGUBERN_API_LISTEN(t *testing.T) {
	cases := []struct {
		env, base string
		port      int
	}{
		{"", "http://127.0.0.1:9090", 9090},
		{"127.0.0.1:9191", "http://127.0.0.1:9191", 9191},
		{":8080", "http://127.0.0.1:8080", 8080},
		{"0.0.0.0:7070", "http://127.0.0.1:7070", 7070},
		{"[::1]:9292", "http://[::1]:9292", 9292},
		{"192.168.1.5:9090", "http://127.0.0.1:9090", 9090}, // /internal needs loopback
		{"garbage", "http://127.0.0.1:9090", 9090},
		{"127.0.0.1:99999", "http://127.0.0.1:9090", 9090},
	}
	for _, tc := range cases {
		t.Setenv("GUBERN_API_LISTEN", tc.env)
		if got := defaultAPIBase(); got != tc.base {
			t.Errorf("GUBERN_API_LISTEN=%q: base = %q, want %q", tc.env, got, tc.base)
		}
		if got := defaultAPIPort(); got != tc.port {
			t.Errorf("GUBERN_API_LISTEN=%q: port = %d, want %d", tc.env, got, tc.port)
		}
	}
}

func TestClientCommands_DefaultToListenPort(t *testing.T) {
	t.Setenv("GUBERN_API_LISTEN", "127.0.0.1:9191")
	for _, c := range []struct {
		name string
		flag string
		want string
	}{
		{"stop", "api-port", "9191"},
		{"status", "api-port", "9191"},
		{"reload", "api-port", "9191"},
		{"ui", "addr", "http://127.0.0.1:9191"},
		{"code", "addr", "http://127.0.0.1:9191"},
	} {
		cmd, _, err := NewRootCommand().Find([]string{c.name})
		if err != nil {
			t.Fatal(err)
		}
		f := cmd.Flags().Lookup(c.flag)
		if f == nil || f.DefValue != c.want {
			t.Errorf("%s --%s default = %v, want %s", c.name, c.flag, f, c.want)
		}
	}
}
