package wrapperprotocol

import "testing"

func TestCheckRefusesWhatCannotBeOffered(t *testing.T) {
	good := Description{Tool: Tool{Slug: "acme", Name: "Acme"}, Version: "1", Contract: ContractVersion, Image: "acme:dev", Command: []string{"/acme"},
		Connection: ConnectionSpec{Credentials: []Field{{Name: "ACME_KEY", Label: "Key"}}},
		Claims:     map[string]string{"read_total": "supported"}}
	if why := good.Check(); why != "" {
		t.Fatalf("a good wrapper was refused: %s", why)
	}
	for name, mutate := range map[string]func(*Description){
		"bad slug":          func(d *Description) { d.Tool.Slug = "Acme Tool" },
		"no image":          func(d *Description) { d.Image = "" },
		"no command":        func(d *Description) { d.Command = nil },
		"an empty argument": func(d *Description) { d.Command = []string{"/acme", ""} },
		"asks for nothing":  func(d *Description) { d.Connection = ConnectionSpec{} },
		"claims nothing":    func(d *Description) { d.Claims = nil },
		"lower-case secret": func(d *Description) { d.Connection.Credentials[0].Name = "acme_key" },
		"unknown level":     func(d *Description) { d.Claims = map[string]string{"read_total": "maybe"} },
		"authorized, unclaimed": func(d *Description) { d.Connection.Authorize = true },
		"a setting, unnamed":    func(d *Description) { d.Connection.Config = []Setting{{Name: "Who Sees", Label: "x"}} },
		"a setting, unlabelled": func(d *Description) { d.Connection.Config = []Setting{{Name: "visibility"}} },
		"a setting twice": func(d *Description) {
			d.Connection.Config = []Setting{{Name: "visibility", Label: "a"}, {Name: "visibility", Label: "b"}}
		},
		"a default not offered": func(d *Description) {
			d.Connection.Config = []Setting{{Name: "visibility", Label: "Who sees it", Values: []string{"public"}, Default: "private"}}
		},
	} {
		d := good
		d.Connection.Credentials = append([]Field(nil), good.Connection.Credentials...)
		mutate(&d)
		if d.Check() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A project's config is held to what the wrapper declares: no setting it
// does not take, and no value it does not accept.
func TestAConnectionsConfigIsHeldToTheDeclaredSettings(t *testing.T) {
	spec := ConnectionSpec{Credentials: []Field{{Name: "K", Label: "k"}}, Config: []Setting{
		{Name: "visibility", Label: "Who sees a post", Values: []string{"public", "unlisted", "private"}, Default: "private"},
		{Name: "signature", Label: "Signed as"},
	}}
	if why := spec.CheckConfig(map[string]any{"visibility": "public", "signature": "the Pong team"}); why != "" {
		t.Errorf("a good config: %s", why)
	}
	for name, cfg := range map[string]map[string]any{
		"an undeclared setting": {"audience": "everyone"},
		"a value not taken":     {"visibility": "everyone"},
		"not text":              {"visibility": true},
	} {
		if spec.CheckConfig(cfg) == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	good := Description{Tool: Tool{Slug: "acme", Name: "Acme"}, Version: "1", Contract: ContractVersion, Image: "a:dev",
		Command: []string{"/a"}, Connection: spec, Claims: map[string]string{"publish": "supported"}}
	if why := good.Check(); why != "" {
		t.Errorf("a wrapper.json declaring settings: %s", why)
	}
}
