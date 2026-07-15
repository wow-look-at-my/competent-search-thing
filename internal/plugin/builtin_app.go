package plugin

import "context"

// builtinAppID is the provider id of the app-commands builtin.
const builtinAppID = "app"

// appCommand describes one app-control bang.
type appCommand struct {
	bang     string
	title    string
	subtitle string
	icon     string
}

// appCommandProvider serves the app-control bangs (!rescan, !reload,
// !config, !version, !quit). Targeted-only: each bang yields exactly
// one result whose action is the internal run_builtin type, executed
// by the app's builtin dispatch table.
type appCommandProvider struct {
	builtinBase
	commands map[string]appCommand
}

func newAppCommandProvider(version string) *appCommandProvider {
	if version == "" {
		version = "dev"
	}
	table := []appCommand{
		{bang: "rescan", title: "Rescan file index", subtitle: "Rebuild the index from disk now", icon: "bolt"},
		{bang: "reload", title: "Reload plugins", subtitle: "Re-read plugin manifests and restart providers", icon: "bolt"},
		{bang: "config", title: "Open config file", subtitle: "Open config.json", icon: "text"},
		{bang: "version", title: "Show version", subtitle: "competent-search-thing " + version, icon: "info"},
		{bang: "quit", title: "Quit", subtitle: "Close competent-search-thing", icon: "warning"},
	}
	p := &appCommandProvider{
		builtinBase: builtinBase{pid: builtinAppID, name: "App Commands"},
		commands:    make(map[string]appCommand, len(table)),
	}
	for _, c := range table {
		p.builtinBase.bangs = append(p.builtinBase.bangs, c.bang)
		p.commands[c.bang] = c
	}
	return p
}

func (p *appCommandProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	if !req.Targeted {
		return nil, nil, nil
	}
	c, ok := p.commands[req.Bang]
	if !ok {
		return nil, nil, nil
	}
	score := float64(100)
	return []Result{{
		Title:    c.title,
		Subtitle: c.subtitle,
		Icon:     c.icon,
		Score:    &score,
		Action:   &Action{Type: ActionRunBuiltin, Value: c.bang},
	}}, nil, nil
}
