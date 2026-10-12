package config

import (
	"slices"
	"testing"

	"loods/internal/project"
)

func TestApplyConfig(t *testing.T) {
	it := &project.Item{Rel: "g/p", Commands: []project.Command{
		{Name: "dev", Run: "npm run dev"},
		{Name: "compose", Run: "docker compose up"},
	}}
	Config{Projects: map[string]ProjectSpec{"g/p": {
		Default: "web",
		Commands: map[string]CommandSpec{
			"compose": {Hide: true},
			"dev":     {Run: "npm run dev -- --port 4000"},
			"web":     {Run: "make web", URL: "http://localhost:4000"},
		},
	}}}.Apply([]*project.Item{it})
	var got []string
	for _, c := range it.Commands {
		got = append(got, c.Name+"="+c.Run)
	}
	if want := []string{"dev=npm run dev -- --port 4000", "web=make web"}; !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if it.DefaultCommand != "web" {
		t.Errorf("default = %q", it.DefaultCommand)
	}
}
